package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// DeliveryStatus represents the status of a delivery
type DeliveryStatus string

const (
	DeliveryStatusPending   DeliveryStatus = "pending"
	DeliveryStatusDelivered DeliveryStatus = "delivered"
	DeliveryStatusFailed    DeliveryStatus = "failed"
)

// DeliveryEvent represents an event that needs to be delivered
type DeliveryEvent struct {
	ID           string                 `json:"id"`
	UserID       string                 `json:"user_id"`
	Token        string                 `json:"token"`
	EventType    string                 `json:"event_type"`
	Payload      map[string]interface{} `json:"payload"`
	JsonData     []byte                 `json:"json_data"`
	FilePath     string                 `json:"file_path,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	AttemptCount int                    `json:"attempt_count"`
	Status       DeliveryStatus         `json:"status"`
	LastError    string                 `json:"last_error,omitempty"`
}

// DeliveryResult represents the result of a delivery attempt
type DeliveryResult struct {
	Channel   string    `json:"channel"` // "webhook", "global_webhook", "rabbitmq"
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Duration  int64     `json:"duration_ms"`
	Timestamp time.Time `json:"timestamp"`
}

// DeliveryManager manages reliable event delivery to multiple channels
type DeliveryManager struct {
	mu            sync.RWMutex
	pendingEvents map[string]*DeliveryEvent
	maxRetries    int
	retryBackoff  time.Duration
	timeout       time.Duration
}

var deliveryManager *DeliveryManager

// InitDeliveryManager initializes the global delivery manager
func InitDeliveryManager() {
	deliveryManager = &DeliveryManager{
		pendingEvents: make(map[string]*DeliveryEvent),
		maxRetries:    3,
		retryBackoff:  2 * time.Second,
		timeout:       10 * time.Second, // Reduced for high scale
	}

	// Start background processor for retry logic
	go deliveryManager.processRetries()

	log.Info().
		Int("maxRetries", deliveryManager.maxRetries).
		Dur("timeout", deliveryManager.timeout).
		Msg("Delivery manager initialized")
}

// DeliverEvent delivers an event to all configured channels with guaranteed delivery
func (dm *DeliveryManager) DeliverEvent(event *DeliveryEvent) {
	event.CreatedAt = time.Now()
	event.Status = DeliveryStatusPending

	// Generate unique ID if not provided
	if event.ID == "" {
		event.ID = fmt.Sprintf("%s_%d", event.UserID, time.Now().UnixNano())
	}

	// Store event for retry tracking
	dm.mu.Lock()
	dm.pendingEvents[event.ID] = event
	dm.mu.Unlock()

	log.Info().
		Str("eventID", event.ID).
		Str("userID", event.UserID).
		Str("eventType", event.EventType).
		Msg("Starting parallel delivery")

	// Process delivery in background to avoid blocking
	go dm.processDelivery(event)
}

// processDelivery handles the actual delivery to all channels
func (dm *DeliveryManager) processDelivery(event *DeliveryEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), dm.timeout)
	defer cancel()

	var wg sync.WaitGroup
	results := make(chan DeliveryResult, 3) // Maximum 3 channels

	// Get user webhook URL
	webhookURL := getUserWebhookUrl(event.Token)

	// Channel 1: User Webhook (if configured)
	if webhookURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := dm.deliverToUserWebhook(ctx, event, webhookURL)
			results <- result
		}()
	}

	// Channel 2: Global Webhook (if configured)
	if *globalWebhook != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := dm.deliverToGlobalWebhook(ctx, event)
			results <- result
		}()
	}

	// Channel 3: RabbitMQ (if enabled)
	if rabbitEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := dm.deliverToRabbitMQ(ctx, event)
			results <- result
		}()
	}

	// Wait for all deliveries to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var deliveryResults []DeliveryResult
	allSuccess := true

	for result := range results {
		deliveryResults = append(deliveryResults, result)
		if !result.Success {
			allSuccess = false
		}

		log.Debug().
			Str("eventID", event.ID).
			Str("channel", result.Channel).
			Bool("success", result.Success).
			Int64("durationMs", result.Duration).
			Str("error", result.Error).
			Msg("Channel delivery result")
	}

	// Update event status
	dm.mu.Lock()
	if allSuccess {
		event.Status = DeliveryStatusDelivered
		delete(dm.pendingEvents, event.ID) // Remove from pending
		log.Info().
			Str("eventID", event.ID).
			Int("channelsDelivered", len(deliveryResults)).
			Msg("Event successfully delivered to all channels")
	} else {
		event.AttemptCount++
		if event.AttemptCount >= dm.maxRetries {
			event.Status = DeliveryStatusFailed
			event.LastError = "Max retries exceeded"
			delete(dm.pendingEvents, event.ID)
			log.Error().
				Str("eventID", event.ID).
				Int("attemptCount", event.AttemptCount).
				Msg("Event delivery failed permanently")
		} else {
			log.Warn().
				Str("eventID", event.ID).
				Int("attemptCount", event.AttemptCount).
				Int("maxRetries", dm.maxRetries).
				Msg("Event delivery partially failed, will retry")
		}
	}
	dm.mu.Unlock()
}

// deliverToUserWebhook delivers event to user webhook
func (dm *DeliveryManager) deliverToUserWebhook(ctx context.Context, event *DeliveryEvent, webhookURL string) DeliveryResult {
	start := time.Now()
	result := DeliveryResult{
		Channel:   "webhook",
		Timestamp: start,
	}

	// Get user's HTTP client with timeout
	client := clientManager.GetHTTPClient(event.UserID)
	if client == nil {
		result.Success = false
		result.Error = "HTTP client not found for user"
		result.Duration = time.Since(start).Milliseconds()
		return result
	}

	// Set context timeout for this request
	client.SetTimeout(5 * time.Second) // Aggressive timeout for high scale

	// Prepare data
	instanceName := ""
	userinfo, found := userinfocache.Get(event.Token)
	if found {
		instanceName = userinfo.(Values).Get("Name")
	}

	data := map[string]string{
		"jsonData":     string(event.JsonData),
		"token":        event.Token,
		"instanceName": instanceName,
	}

	// Send request
	var err error

	if event.FilePath == "" {
		// Regular webhook
		_, err = client.R().
			SetContext(ctx).
			SetFormData(data).
			Post(webhookURL)
	} else {
		// File webhook
		err = callHookFileWithContext(ctx, webhookURL, data, event.UserID, event.FilePath)
	}

	result.Duration = time.Since(start).Milliseconds()

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		log.Error().
			Err(err).
			Str("eventID", event.ID).
			Str("webhookURL", webhookURL).
			Msg("User webhook delivery failed")
	} else {
		result.Success = true
		log.Debug().
			Str("eventID", event.ID).
			Str("webhookURL", webhookURL).
			Int64("durationMs", result.Duration).
			Msg("User webhook delivered successfully")
	}

	return result
}

// deliverToGlobalWebhook delivers event to global webhook
func (dm *DeliveryManager) deliverToGlobalWebhook(ctx context.Context, event *DeliveryEvent) DeliveryResult {
	start := time.Now()
	result := DeliveryResult{
		Channel:   "global_webhook",
		Timestamp: start,
	}

	instanceName := ""
	userinfo, found := userinfocache.Get(event.Token)
	if found {
		instanceName = userinfo.(Values).Get("Name")
	}

	globalData := map[string]string{
		"jsonData":     string(event.JsonData),
		"token":        event.Token,
		"userID":       event.UserID,
		"instanceName": instanceName,
	}

	// Use a generic HTTP client for global webhook
	client := clientManager.GetHTTPClient(event.UserID)
	if client != nil {
		client.SetTimeout(5 * time.Second)
		_, err := client.R().
			SetContext(ctx).
			SetFormData(globalData).
			Post(*globalWebhook)

		result.Duration = time.Since(start).Milliseconds()

		if err != nil {
			result.Success = false
			result.Error = err.Error()
			log.Error().
				Err(err).
				Str("eventID", event.ID).
				Str("globalWebhookURL", *globalWebhook).
				Msg("Global webhook delivery failed")
		} else {
			result.Success = true
			log.Debug().
				Str("eventID", event.ID).
				Int64("durationMs", result.Duration).
				Msg("Global webhook delivered successfully")
		}
	} else {
		result.Success = false
		result.Error = "HTTP client not available"
		result.Duration = time.Since(start).Milliseconds()
	}

	return result
}

// deliverToRabbitMQ delivers event to RabbitMQ
func (dm *DeliveryManager) deliverToRabbitMQ(ctx context.Context, event *DeliveryEvent) DeliveryResult {
	start := time.Now()
	result := DeliveryResult{
		Channel:   "rabbitmq",
		Timestamp: start,
	}

	// Check context timeout
	select {
	case <-ctx.Done():
		result.Success = false
		result.Error = "Context timeout"
		result.Duration = time.Since(start).Milliseconds()
		return result
	default:
	}

	// Send to RabbitMQ with instance information
	err := sendToGlobalRabbitWithInstanceInfoSync(event.JsonData, event.EventType, event.UserID, event.Token)
	result.Duration = time.Since(start).Milliseconds()

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		log.Error().
			Err(err).
			Str("eventID", event.ID).
			Str("eventType", event.EventType).
			Msg("RabbitMQ delivery failed")
	} else {
		result.Success = true
		log.Debug().
			Str("eventID", event.ID).
			Str("eventType", event.EventType).
			Int64("durationMs", result.Duration).
			Msg("RabbitMQ delivered successfully")
	}

	return result
}

// processRetries handles retry logic for failed deliveries
func (dm *DeliveryManager) processRetries() {
	ticker := time.NewTicker(dm.retryBackoff)
	defer ticker.Stop()

	for range ticker.C {
		dm.retryFailedEvents()
	}
}

// retryFailedEvents retries events that are still pending
func (dm *DeliveryManager) retryFailedEvents() {
	dm.mu.RLock()
	eventsToRetry := make([]*DeliveryEvent, 0)

	for _, event := range dm.pendingEvents {
		if event.Status == DeliveryStatusPending &&
			event.AttemptCount < dm.maxRetries &&
			time.Since(event.CreatedAt) > dm.retryBackoff {
			eventsToRetry = append(eventsToRetry, event)
		}
	}
	dm.mu.RUnlock()

	for _, event := range eventsToRetry {
		log.Info().
			Str("eventID", event.ID).
			Int("attemptCount", event.AttemptCount).
			Msg("Retrying failed event delivery")
		go dm.processDelivery(event)
	}
}

// GetPendingEventsCount returns the number of pending events
func (dm *DeliveryManager) GetPendingEventsCount() int {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return len(dm.pendingEvents)
}

// GetEventStatus returns the status of a specific event
func (dm *DeliveryManager) GetEventStatus(eventID string) (*DeliveryEvent, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	event, exists := dm.pendingEvents[eventID]
	return event, exists
}
