package handler

import (
	"encoding/json"
	"net/http"
)

// writeJSON sends body as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent at this point, so if encoding fails
	// (e.g. the client disconnected) there is nothing useful left to do.
	_ = json.NewEncoder(w).Encode(body)
}
