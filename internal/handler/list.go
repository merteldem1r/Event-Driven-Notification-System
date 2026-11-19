package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// listResponse is one page of the list endpoint.
type listResponse struct {
	Items      []notificationResponse `json:"items"`
	NextCursor *string                `json:"next_cursor,omitempty"` // absent on the last page
}

// ListNotifications handles GET /api/v1/notifications: filtered, newest first, cursor-paginated (D6).
func ListNotifications(svc NotificationService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, errs := parseListFilter(r.URL.Query())
		if len(errs) > 0 {
			// Also report rule violations in the values that did parse, so the client sees every error at once.
			var ve *domain.ValidationError
			if errors.As(f.Validate(), &ve) {
				errs = append(errs, ve.Fields...)
			}
			writeServiceError(w, r, logger, &domain.ValidationError{Fields: errs})
			return
		}

		page, err := svc.List(r.Context(), f)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}

		resp := listResponse{Items: make([]notificationResponse, len(page.Items))} // [] rather than null when empty
		for i, n := range page.Items {
			resp.Items[i] = toNotificationResponse(n)
		}
		if page.Next != nil {
			c := encodeCursor(*page.Next)
			resp.NextCursor = &c
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// parseListFilter reads the query string and reports values it can't parse. An unparsable
// value is left unset, so it isn't reported twice; rules like the limit range are in
// domain.ListFilter.Validate.
func parseListFilter(q url.Values) (domain.ListFilter, []domain.FieldError) {
	f := domain.ListFilter{Limit: domain.DefaultListLimit}
	var errs []domain.FieldError
	bad := func(field, msg string) { errs = append(errs, domain.FieldError{Field: field, Message: msg}) }

	if v := q.Get("status"); v != "" {
		s := domain.Status(v)
		f.Status = &s
	}
	if v := q.Get("channel"); v != "" {
		c := domain.Channel(v)
		f.Channel = &c
	}
	if v := q.Get("batch_id"); v != "" {
		if id, err := uuid.Parse(v); err != nil {
			bad("batch_id", "must be a UUID")
		} else {
			f.BatchID = &id
		}
	}
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.name); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err != nil {
				bad(p.name, "must be an RFC 3339 time, e.g. 2026-11-20T10:00:00Z")
			} else {
				*p.dst = &t
			}
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			bad("limit", "must be a number")
		} else {
			f.Limit = n
		}
	}
	if v := q.Get("cursor"); v != "" {
		if c, err := decodeCursor(v); err != nil {
			bad("cursor", "is invalid; send next_cursor from the previous page unchanged")
		} else {
			f.After = &c
		}
	}
	return f, errs
}

// cursorToken is what a cursor holds. Clients treat the encoded token as opaque,
// so its content can change without breaking them.
type cursorToken struct {
	CreatedAt time.Time `json:"t"` // RFC 3339 with nanoseconds, so no precision is lost
	ID        uuid.UUID `json:"id"`
}

// encodeCursor turns a position into a URL-safe token.
func encodeCursor(c domain.Cursor) string {
	// UTC like every time in the API; the same instant in any zone gives the same token.
	b, _ := json.Marshal(cursorToken{CreatedAt: c.CreatedAt.UTC(), ID: c.ID}) // can't fail: plain time and UUID
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reverses encodeCursor and rejects anything it didn't produce.
func decodeCursor(s string) (domain.Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return domain.Cursor{}, err
	}
	var tok cursorToken
	if err := json.Unmarshal(b, &tok); err != nil {
		return domain.Cursor{}, err
	}
	if tok.CreatedAt.IsZero() || tok.ID == uuid.Nil {
		return domain.Cursor{}, errors.New("incomplete cursor")
	}
	return domain.Cursor{CreatedAt: tok.CreatedAt, ID: tok.ID}, nil
}
