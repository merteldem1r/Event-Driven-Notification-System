package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// bodyErrorMessage turns a JSON decoding error into a message for the client,
// without exposing Go type names.
func bodyErrorMessage(err error) string {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return "request body is empty"
	case errors.As(err, &syntaxErr):
		return fmt.Sprintf("malformed JSON at position %d", syntaxErr.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "malformed JSON"
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return fmt.Sprintf("field %q must be a %s", typeErr.Field, typeErr.Type)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return strings.TrimPrefix(err.Error(), "json: ") // encoding/json has no typed error for this
	default:
		return "request body must be a JSON object"
	}
}
