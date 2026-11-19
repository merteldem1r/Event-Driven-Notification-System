package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
)

// NotificationService is what the notification handlers need from the service layer.
type NotificationService interface {
	Create(ctx context.Context, n domain.Notification) (_ domain.Notification, replayed bool, _ error)
	CreateBatch(ctx context.Context, ns []domain.Notification, idempotencyKey *string) (_ domain.Batch, replayed bool, _ error)
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

// toDomain maps the request to a notification, applying the priority default (D4).
// An unknown priority name becomes 0, which validation rejects.
func (req createNotificationRequest) toDomain() domain.Notification {
	priority := domain.PriorityNormal
	if req.Priority != "" {
		priority = domain.ParsePriority(req.Priority)
	}
	return domain.Notification{
		Channel:   domain.Channel(req.Channel),
		Recipient: req.Recipient,
		Content:   req.Content,
		Priority:  priority,
	}
}

type createBatchRequest struct {
	Notifications []createNotificationRequest `json:"notifications"`
}

type batchCreatedResponse struct {
	BatchID uuid.UUID   `json:"batch_id"`
	Count   int         `json:"count"`
	IDs     []uuid.UUID `json:"ids"` // ids[i] belongs to the request's notifications[i]
}

// idempotencyKey returns the request's Idempotency-Key, or nil if the header is missing.
// Values (not Get) tells "missing" apart from "sent empty", which validation rejects.
func idempotencyKey(r *http.Request) *string {
	if keys := r.Header.Values(headerIdempotencyKey); len(keys) > 0 {
		return &keys[0]
	}
	return nil
}

// correlationID returns the request's correlation ID for storing on notifications, or nil.
func correlationID(r *http.Request) *string {
	if id := logging.CorrelationID(r.Context()); id != "" {
		return &id
	}
	return nil
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
	CorrelationID     *string    `json:"correlation_id,omitempty"` // of the request that created it
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
		CorrelationID:     n.CorrelationID,
		CreatedAt:         n.CreatedAt.UTC(), // same time zone for every client
		UpdatedAt:         n.UpdatedAt.UTC(),
	}
}

// CreateNotification handles POST /api/v1/notifications: it validates and queues one notification.
func CreateNotification(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createNotificationRequest
		if !decodeJSON(w, r, &req, maxBodyBytes) {
			return
		}

		n := req.toDomain()
		n.IdempotencyKey = idempotencyKey(r)
		n.CorrelationID = correlationID(r) // stored so the worker can log with the same ID later

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

// CreateNotificationBatch handles POST /api/v1/notifications/batch: up to 1000 notifications,
// saved all together or not at all (D5).
func CreateNotificationBatch(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createBatchRequest
		if !decodeJSON(w, r, &req, maxBatchBodyBytes) {
			return
		}

		corrID := correlationID(r)
		ns := make([]domain.Notification, len(req.Notifications))
		for i, item := range req.Notifications {
			ns[i] = item.toDomain()
			ns[i].CorrelationID = corrID
		}

		batch, replayed, err := svc.CreateBatch(r.Context(), ns, idempotencyKey(r))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}

		ids := make([]uuid.UUID, len(batch.Notifications))
		for i, n := range batch.Notifications {
			ids[i] = n.ID
		}

		// One line for the batch, not one per notification.
		msg := "batch created"
		if replayed {
			msg = "idempotent batch replayed"
			w.Header().Set(headerIdempotentReplayed, "true")
		}
		logger.InfoContext(r.Context(), msg, "batch_id", batch.ID, "count", len(ids))

		w.Header().Set("Location", "/api/v1/batches/"+batch.ID.String())
		writeJSON(w, http.StatusAccepted, batchCreatedResponse{BatchID: batch.ID, Count: len(ids), IDs: ids})
	}
}
