package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// Error codes are part of the API: clients can switch on them, so they don't change.
const (
	codeInvalidBody      = "invalid_body"
	codeBodyTooLarge     = "body_too_large"
	codeValidationFailed = "validation_failed"
	codeInvalidID        = "invalid_id"
	codeNotFound         = "not_found"
	codeInternal         = "internal_error"
)

// errorResponse is the one error format of the API: {"error": {code, message, details}}.
type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string             `json:"code"`
	Message string             `json:"message"`
	Details []fieldErrorDetail `json:"details,omitempty"`
}

type fieldErrorDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// writeJSON writes body as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // status already sent; nothing to do on error
}

// writeError writes an error response without field details.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}

// writeServiceError maps an error from the service layer to a status code and error response.
func writeServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		details := make([]fieldErrorDetail, len(ve.Fields))
		for i, f := range ve.Fields {
			details[i] = fieldErrorDetail{Field: f.Field, Message: f.Message}
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: errorBody{
			Code: codeValidationFailed, Message: "the request has invalid fields", Details: details,
		}})
		return
	}

	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "the requested item was not found")
		return
	}

	// Unexpected error: log the cause, but don't show internals to the client.
	logger.ErrorContext(r.Context(), "request failed", "error", err)
	writeError(w, http.StatusInternalServerError, codeInternal, "something went wrong")
}
