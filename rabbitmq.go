package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog/log"
)

var (
	rabbitConn           *amqp091.Connection
	rabbitChannel        *amqp091.Channel
	rabbitEnabled        bool
	rabbitOnce           sync.Once
	rabbitQueue          string
	rabbitQueuePrefix    string
	rabbitSpecificEvents map[string]bool
)

// Call this in main() or initialization
func InitRabbitMQ() {
	rabbitURL := os.Getenv("RABBITMQ_URL")
	rabbitQueue = os.Getenv("RABBITMQ_QUEUE")
	if rabbitQueue == "" {
		rabbitQueue = "whatsapp_events" // default queue
	}

	// Initialize queue prefix
	rabbitQueuePrefix = os.Getenv("RABBITMQ_QUEUE_PREFIX")
	if rabbitQueuePrefix == "" {
		rabbitQueuePrefix = "wuzapi" // default prefix
	}

	// Initialize specific events map
	rabbitSpecificEvents = make(map[string]bool)
	specificEventsStr := os.Getenv("AMQP_SPECIFIC_EVENTS")
	if specificEventsStr != "" {
		events := strings.Split(specificEventsStr, ",")
		for _, event := range events {
			rabbitSpecificEvents[strings.TrimSpace(event)] = true
		}
		log.Info().
			Interface("specificEvents", rabbitSpecificEvents).
			Msg("Specific RabbitMQ events configured")
	}

	if rabbitURL == "" {
		rabbitEnabled = false
		log.Info().Msg("RABBITMQ_URL is not set. RabbitMQ publishing disabled.")
		return
	}
	var err error
	rabbitConn, err = amqp091.Dial(rabbitURL)
	if err != nil {
		rabbitEnabled = false
		log.Error().Err(err).Msg("Could not connect to RabbitMQ")
		return
	}
	rabbitChannel, err = rabbitConn.Channel()
	if err != nil {
		rabbitEnabled = false
		log.Error().Err(err).Msg("Could not open RabbitMQ channel")
		return
	}
	rabbitEnabled = true
	log.Info().
		Str("queue", rabbitQueue).
		Str("prefix", rabbitQueuePrefix).
		Msg("RabbitMQ connection established.")
}

// getQueueName returns the appropriate queue name based on event type
func getQueueName(eventType string) string {
	// Check if this event type should have a specific queue
	if rabbitSpecificEvents[eventType] {
		return rabbitQueuePrefix + "_" + strings.ToLower(eventType)
	}
	// Use default queue with prefix
	return rabbitQueuePrefix + "_" + rabbitQueue
}

// Optionally, allow overriding the queue per message
func PublishToRabbit(data []byte, queueOverride ...string) error {
	if !rabbitEnabled {
		return nil
	}
	queueName := rabbitQueue
	if len(queueOverride) > 0 && queueOverride[0] != "" {
		queueName = queueOverride[0]
	}
	// Declare queue (idempotent)
	_, err := rabbitChannel.QueueDeclare(
		queueName,
		true,  // durable
		false, // auto-delete
		false, // exclusive
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		log.Error().Err(err).Str("queue", queueName).Msg("Could not declare RabbitMQ queue")
		return err
	}
	err = rabbitChannel.Publish(
		"",        // exchange (default)
		queueName, // routing key = queue
		false,     // mandatory
		false,     // immediate
		amqp091.Publishing{
			ContentType: "application/json",
			Body:        data,
		},
	)
	if err != nil {
		log.Error().Err(err).Str("queue", queueName).Msg("Could not publish to RabbitMQ")
	} else {
		log.Debug().Str("queue", queueName).Msg("Published message to RabbitMQ")
	}
	return err
}

// Usage - like sendToGlobalWebhook
func sendToGlobalRabbit(jsonData []byte, eventType string, queueOverride ...string) {
	if !rabbitEnabled {
		log.Debug().Msg("RabbitMQ publishing is disabled, not sending message")
		return
	}

	var queueName string
	if len(queueOverride) > 0 && queueOverride[0] != "" {
		// Use provided queue override
		queueName = queueOverride[0]
	} else {
		// Determine queue name based on event type
		queueName = getQueueName(eventType)
	}

	err := PublishToRabbit(jsonData, queueName)
	if err != nil {
		log.Error().Err(err).
			Str("eventType", eventType).
			Str("queue", queueName).
			Msg("Failed to publish to RabbitMQ")
	} else {
		log.Debug().
			Str("eventType", eventType).
			Str("queue", queueName).
			Msg("Published message to RabbitMQ")
	}
}

// Enhanced version that includes instance information in the payload
func sendToGlobalRabbitWithInstanceInfo(originalJsonData []byte, eventType string, userID string, token string, queueOverride ...string) {
	if !rabbitEnabled {
		log.Debug().Msg("RabbitMQ publishing is disabled, not sending message")
		return
	}

	// Get instance name and ownerId from cache if available
	instanceName := ""
	ownerId := ""
	userinfo, found := userinfocache.Get(token)
	if found {
		instanceName = userinfo.(Values).Get("Name")
		ownerId = userinfo.(Values).Get("Jid")
	}

	// Parse original JSON data
	var originalEvent map[string]interface{}
	err := json.Unmarshal(originalJsonData, &originalEvent)
	if err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal original JSON data for RabbitMQ")
		return
	}

	// Create enhanced payload with instance information
	enhancedPayload := map[string]interface{}{
		"event":        originalEvent,
		"instanceId":   userID,
		"instanceName": instanceName,
		"token":        token,
		"ownerId":      ownerId,
	}

	// Marshal enhanced payload
	enhancedJsonData, err := json.Marshal(enhancedPayload)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal enhanced payload for RabbitMQ")
		return
	}

	var queueName string
	if len(queueOverride) > 0 && queueOverride[0] != "" {
		// Use provided queue override
		queueName = queueOverride[0]
	} else {
		// Determine queue name based on event type
		queueName = getQueueName(eventType)
	}

	err = PublishToRabbit(enhancedJsonData, queueName)
	if err != nil {
		log.Error().Err(err).
			Str("eventType", eventType).
			Str("queue", queueName).
			Str("instanceId", userID).
			Str("instanceName", instanceName).
			Str("ownerId", ownerId).
			Msg("Failed to publish to RabbitMQ")
	} else {
		log.Debug().
			Str("eventType", eventType).
			Str("queue", queueName).
			Str("instanceId", userID).
			Str("instanceName", instanceName).
			Str("ownerId", ownerId).
			Msg("Published enhanced message to RabbitMQ")
	}
}
