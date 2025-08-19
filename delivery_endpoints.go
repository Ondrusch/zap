package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog/log"
)

// DeliveryStatus endpoint to check delivery manager status
func (s *server) DeliveryStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deliveryManager == nil {
			s.Respond(w, r, http.StatusServiceUnavailable, "Delivery manager not initialized")
			return
		}

		pendingCount := deliveryManager.GetPendingEventsCount()

		status := map[string]interface{}{
			"status":           "running",
			"pending_events":   pendingCount,
			"max_retries":      deliveryManager.maxRetries,
			"timeout_ms":       deliveryManager.timeout.Milliseconds(),
			"retry_backoff_ms": deliveryManager.retryBackoff.Milliseconds(),
		}

		responseJson, err := json.Marshal(status)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseJson)
	}
}

// EventStatus endpoint to check specific event status
func (s *server) EventStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deliveryManager == nil {
			s.Respond(w, r, http.StatusServiceUnavailable, "Delivery manager not initialized")
			return
		}

		vars := mux.Vars(r)
		eventID := vars["eventId"]

		if eventID == "" {
			s.Respond(w, r, http.StatusBadRequest, "Event ID is required")
			return
		}

		event, exists := deliveryManager.GetEventStatus(eventID)
		if !exists {
			s.Respond(w, r, http.StatusNotFound, "Event not found or already completed")
			return
		}

		responseJson, err := json.Marshal(event)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseJson)
	}
}

// DeliveryMetrics endpoint for monitoring and debugging
func (s *server) DeliveryMetrics() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deliveryManager == nil {
			s.Respond(w, r, http.StatusServiceUnavailable, "Delivery manager not initialized")
			return
		}

		// Get query parameters for filtering
		userID := r.URL.Query().Get("user_id")
		limitStr := r.URL.Query().Get("limit")

		limit := 50 // default limit
		if limitStr != "" {
			if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
				limit = parsedLimit
			}
		}

		deliveryManager.mu.RLock()
		events := make([]*DeliveryEvent, 0)
		count := 0

		for _, event := range deliveryManager.pendingEvents {
			if userID == "" || event.UserID == userID {
				if count < limit {
					events = append(events, event)
				}
				count++
			}
		}
		deliveryManager.mu.RUnlock()

		metrics := map[string]interface{}{
			"total_pending":  len(deliveryManager.pendingEvents),
			"filtered_count": count,
			"shown_count":    len(events),
			"events":         events,
		}

		responseJson, err := json.Marshal(metrics)
		if err != nil {
			s.Respond(w, r, http.StatusInternalServerError, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseJson)
	}
}

// ForceRetry endpoint to manually retry failed events
func (s *server) ForceRetry() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deliveryManager == nil {
			s.Respond(w, r, http.StatusServiceUnavailable, "Delivery manager not initialized")
			return
		}

		vars := mux.Vars(r)
		eventID := vars["eventId"]

		if eventID == "" {
			// Retry all pending events
			deliveryManager.retryFailedEvents()
			s.Respond(w, r, http.StatusOK, "Retry triggered for all pending events")
			return
		}

		// Retry specific event
		event, exists := deliveryManager.GetEventStatus(eventID)
		if !exists {
			s.Respond(w, r, http.StatusNotFound, "Event not found")
			return
		}

		// Reset attempt count and process
		event.AttemptCount = 0
		event.Status = DeliveryStatusPending
		go deliveryManager.processDelivery(event)

		log.Info().
			Str("eventID", eventID).
			Msg("Manual retry triggered for event")

		s.Respond(w, r, http.StatusOK, "Retry triggered for event: "+eventID)
	}
}
