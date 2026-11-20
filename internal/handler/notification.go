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
	GetBatch(ctx context.Context, batchID uuid.UUID) (domain.BatchSummary, error)
	List(ctx context.Context, f domain.ListFilter) (domain.Page, error)
	Cancel(ctx context.Context, id uuid.UUID) (domain.Notification, error)
	CancelBatch(ctx context.Context, batchID uuid.UUID) (domain.BatchCancellation, error)
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

	ScheduledAt *scheduleTime `json:"scheduled_at"` // optional: send at this time (D21)
}

// toDomain maps the request to a notification, applying the priority default (D4).
// An unknown priority name becomes 0, which validation rejects.
func (req createNotificationRequest) toDomain() domain.Notification {
	priority := domain.PriorityNormal
	if req.Priority != "" {
		priority = domain.ParsePriority(req.Priority)
	}
	var scheduledAt *time.Time
	if req.ScheduledAt != nil {
		t := req.ScheduledAt.Time
		scheduledAt = &t
	}
	return domain.Notification{
		Channel:     domain.Channel(req.Channel),
		Recipient:   req.Recipient,
		Content:     req.Content,
		Priority:    priority,
		ScheduledAt: scheduledAt,
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

// batchStatusResponse is how a batch's progress looks in the API.
type batchStatusResponse struct {
	BatchID   uuid.UUID    `json:"batch_id"`
	Total     int          `json:"total"`
	Counts    statusCounts `json:"counts"`
	CreatedAt time.Time    `json:"created_at"`
}

// batchCancelResponse is the batch status after a cancel, plus how many this request cancelled.
type batchCancelResponse struct {
	batchStatusResponse
	Cancelled int `json:"cancelled"`
}

// statusCounts has a field per status, so every count is always present.
type statusCounts struct {
	Pending    int `json:"pending"`
	Processing int `json:"processing"`
	Sent       int `json:"sent"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
}

// idParam parses the {id} path parameter. A malformed ID is a client mistake (400),
// not a missing item (404). On failure it writes the error response and returns false.
func idParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidID, "id must be a UUID")
		return uuid.Nil, false
	}
	return id, true
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
	ScheduledAt       *time.Time `json:"scheduled_at,omitempty"`
	LastError         *string    `json:"last_error,omitempty"`
	NextAttemptAt     *time.Time `json:"next_attempt_at,omitempty"` // only while a retry is waiting
	ProviderMessageID *string    `json:"provider_message_id,omitempty"`
	CorrelationID     *string    `json:"correlation_id,omitempty"` // of the request that created it
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func toNotificationResponse(n domain.Notification) notificationResponse {
	var next *time.Time
	if n.Status == domain.StatusPending && n.Attempts > 0 { // failed before, waiting for its retry
		t := n.NextAttemptAt.UTC()
		next = &t
	}
	return notificationResponse{
		ID:                n.ID,
		BatchID:           n.BatchID,
		Channel:           string(n.Channel),
		Recipient:         n.Recipient,
		Content:           n.Content,
		Priority:          n.Priority.String(),
		Status:            string(n.Status),
		Attempts:          n.Attempts,
		ScheduledAt:       utc(n.ScheduledAt),
		LastError:         n.LastError,
		NextAttemptAt:     next,
		ProviderMessageID: n.ProviderMessageID,
		CorrelationID:     n.CorrelationID,
		CreatedAt:         n.CreatedAt.UTC(), // same time zone for every client
		UpdatedAt:         n.UpdatedAt.UTC(),
	}
}

// utc returns t in UTC, or nil.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
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
		id, ok := idParam(w, r)
		if !ok {
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

// CancelNotification handles POST /api/v1/notifications/{id}/cancel: stops a notification
// that hasn't been sent yet (D14).
func CancelNotification(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}

		n, err := svc.Cancel(r.Context(), id)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		logger.InfoContext(r.Context(), "notification cancelled", "notification_id", n.ID)
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

// GetBatch handles GET /api/v1/batches/{id}: how many of the batch's notifications are in each status.
func GetBatch(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}

		summary, err := svc.GetBatch(r.Context(), id)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}

		writeJSON(w, http.StatusOK, toBatchStatusResponse(summary))
	}
}

// CancelBatch handles POST /api/v1/batches/{id}/cancel: cancels every notification of the
// batch that is still pending (D14).
func CancelBatch(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}

		result, err := svc.CancelBatch(r.Context(), id)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		logger.InfoContext(r.Context(), "batch cancelled", "batch_id", id, "cancelled", result.Cancelled)
		writeJSON(w, http.StatusOK, batchCancelResponse{
			batchStatusResponse: toBatchStatusResponse(result.Summary),
			Cancelled:           result.Cancelled,
		})
	}
}

// toBatchStatusResponse maps a summary to its API shape.
func toBatchStatusResponse(s domain.BatchSummary) batchStatusResponse {
	c := s.Counts
	return batchStatusResponse{
		BatchID: s.ID,
		Total:   s.Total,
		Counts: statusCounts{
			Pending:    c[domain.StatusPending],
			Processing: c[domain.StatusProcessing],
			Sent:       c[domain.StatusSent],
			Failed:     c[domain.StatusFailed],
			Cancelled:  c[domain.StatusCancelled],
		},
		CreatedAt: s.CreatedAt.UTC(),
	}
}
