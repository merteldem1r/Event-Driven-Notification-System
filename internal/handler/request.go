package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Body size caps; the largest valid request of each kind is well below them.
const (
	maxBodyBytes      = 1 << 20  // 1 MB: one notification
	maxBatchBodyBytes = 10 << 20 // 10 MB: up to 1000 notifications
)

// decodeJSON reads exactly one JSON object of at most limit bytes from the body into dst,
// rejecting unknown fields. On failure it writes the error response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeBodyTooLarge,
				fmt.Sprintf("request body must be at most %d bytes", limit))
			return false
		}
		writeError(w, http.StatusBadRequest, codeInvalidBody, bodyErrorMessage(err))
		return false
	}

	// Anything after the first object, e.g. `{...}{...}`, is a client mistake.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, codeInvalidBody, "request body must contain a single JSON object")
		return false
	}
	return true
}

// errBadScheduleTime is returned while decoding a scheduled_at that isn't an RFC 3339 time.
var errBadScheduleTime = errors.New(`field "scheduled_at" must be an RFC 3339 time with a time zone, ` +
	`e.g. "2026-11-21T09:00:00Z" or "2026-11-21T12:00:00+03:00"`)

// scheduleTime is the scheduled_at field of a request. It exists only to give a clear message
// for a malformed time; encoding/json would report it without the field name.
type scheduleTime struct{ time.Time }

// UnmarshalJSON accepts a JSON string in RFC 3339 format with a time zone.
func (t *scheduleTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errBadScheduleTime
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return errBadScheduleTime
	}
	t.Time = v
	return nil
}

// bodyErrorMessage turns a JSON decoding error into a message for the client,
// without exposing Go type names.
func bodyErrorMessage(err error) string {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return "request body is empty"
	case errors.Is(err, errBadScheduleTime):
		return err.Error()
	case errors.As(err, &syntaxErr):
		return fmt.Sprintf("malformed JSON at position %d", syntaxErr.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "malformed JSON"
	case errors.As(err, &typeErr) && strings.HasSuffix(typeErr.Field, "variables"): // the map, not the key, is named
		return `field "variables" must be an object of strings, e.g. {"order_id":"1042"}`
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return fmt.Sprintf("field %q must be a %s", typeErr.Field, typeErr.Type)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return strings.TrimPrefix(err.Error(), "json: ") // encoding/json has no typed error for this
	default:
		return "request body must be a JSON object"
	}
}
