package handler

import "net/http"

// Health reports that the process is up and able to answer HTTP requests.
// Later phases extend it to also check that PostgreSQL and Redis are reachable.
func Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
