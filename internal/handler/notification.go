package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// NotificationService is what the notification handlers need from the service layer.
type NotificationService interface {
	Create(ctx context.Context, n domain.Notification) (_ domain.Notification, replayed bool, _ error)
	Get(ctx context.Context, id uuid.UUID) (domain.Notification, error)
}

// Idempotency headers (D12), as in the IETF draft and common APIs.
const (
	headerIdempotencyKey     = "Idempotency-Key"
	headerIdempotentReplayed = "Idempotent-Replayed"
)

type createNotificationRequest struct {
	Channel   string `json:"channel"`
	Recipient string `json:"recipient"`
	Content   string `json:"content"`
	Priority  string `json:"priority"` // optional: high, normal (default) or low
}

// notificationResponse is how a notification looks in the API.
type notificationResponse struct {
	ID                uuid.UUID  `json:"id"`
	BatchID           *uuid.UUID `json:"batch_id,omitempty"`
	Channel           string     `json:"channel"`
	Recipient         string     `json:"recipient"`
	Content           string     `json:"content"`
	Priority          string     `json:"priority"`
	Status            string     `json:"status"`
	Attempts          int        `json:"attempts"`
	LastError         *string    `json:"last_error,omitempty"`
	ProviderMessageID *string    `json:"provider_message_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func toNotificationResponse(n domain.Notification) notificationResponse {
	return notificationResponse{
		ID:                n.ID,
		BatchID:           n.BatchID,
		Channel:           string(n.Channel),
		Recipient:         n.Recipient,
		Content:           n.Content,
		Priority:          n.Priority.String(),
		Status:            string(n.Status),
		Attempts:          n.Attempts,
		LastError:         n.LastError,
		ProviderMessageID: n.ProviderMessageID,
		CreatedAt:         n.CreatedAt.UTC(), // same time zone for every client
		UpdatedAt:         n.UpdatedAt.UTC(),
	}
}

// CreateNotification handles POST /api/v1/notifications: it validates and queues one notification.
func CreateNotification(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createNotificationRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		// D4: priority is optional. An unknown name becomes 0, which validation rejects.
		priority := domain.PriorityNormal
		if req.Priority != "" {
			priority = domain.ParsePriority(req.Priority)
		}

		n := domain.Notification{
			Channel:   domain.Channel(req.Channel),
			Recipient: req.Recipient,
			Content:   req.Content,
			Priority:  priority,
		}
		// Optional. Values (not Get) tells "header missing" apart from "header sent empty".
		if keys := r.Header.Values(headerIdempotencyKey); len(keys) > 0 {
			n.IdempotencyKey = &keys[0]
		}

		created, replayed, err := svc.Create(r.Context(), n)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}

		// No recipient or content in logs: they are personal data.
		msg := "notification created"
		if replayed {
			msg = "idempotent request replayed"
			w.Header().Set(headerIdempotentReplayed, "true")
		}
		logger.InfoContext(r.Context(), msg,
			"notification_id", created.ID, "channel", created.Channel, "priority", created.Priority.String())

		// A retry gets exactly what the first attempt got.
		w.Header().Set("Location", "/api/v1/notifications/"+created.ID.String())
		writeJSON(w, http.StatusAccepted, toNotificationResponse(created))
	}
}

// GetNotification handles GET /api/v1/notifications/{id}: it returns one notification and its status.
func GetNotification(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A malformed ID is a client mistake (400), not a missing notification (404).
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidID, "id must be a UUID")
			return
		}

		n, err := svc.Get(r.Context(), id)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, toNotificationResponse(n))
	}
}
