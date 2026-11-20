// Package fakeprovider is a local stand-in for the external provider (D9). It answers like
// the real one, with a set delay and failure rate, reports how many messages per channel it
// received each second, and measures the busiest one-second window per channel (D17).
package fakeprovider

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// maxBodyBytes caps a request body; real messages are far smaller.
const maxBodyBytes = 1 << 20

// StatsPath is reserved for the measurements: GET reads them, DELETE resets them.
// Every other path answers like the provider.
const StatsPath = "/_stats"

// Config controls how the fake provider behaves.
type Config struct {
	Latency     time.Duration // how long each answer takes
	FailureRate float64       // share of requests answered 503, 0 to 1
}

// Provider is the fake provider's HTTP handler.
type Provider struct {
	cfg    Config
	logger *slog.Logger
	random func() float64 // a number in [0, 1); decides which requests fail

	mu       sync.Mutex
	received map[string]int         // messages per channel since the last report
	recent   map[string][]time.Time // arrivals per channel within the last second
	stats    Stats                  // since the start or the last reset
}

// Stats is what the provider measured per channel since the start or the last reset.
type Stats struct {
	Received map[string]int `json:"received"`   // messages that arrived
	Busiest  map[string]int `json:"busiest_1s"` // most arrivals within any one second
}

// New returns a Provider. cfg is expected to be valid (config.LoadFakeProvider checks it).
func New(cfg Config, logger *slog.Logger) *Provider {
	// Not for security: it only picks which requests fail.
	return &Provider{ //nolint:gosec // see above
		cfg: cfg, logger: logger, random: rand.Float64,
		received: map[string]int{}, recent: map[string][]time.Time{}, stats: newStats(),
	}
}

func newStats() Stats { return Stats{Received: map[string]int{}, Busiest: map[string]int{}} }

// message is the body the real provider expects: {to, channel, content}.
type message struct {
	To      string `json:"to"`
	Channel string `json:"channel"`
	Content string `json:"content"`
}

// ServeHTTP answers POST on any path, like a webhook.site URL: 202 {messageId, status,
// timestamp} after the configured delay, or 503 for the configured share of requests.
func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == StatsPath {
		p.serveStats(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}
	var m message
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&m); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON {to, channel, content}"})
		return
	}
	if m.To == "" || m.Channel == "" || m.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to, channel and content are required"})
		return
	}
	p.count(m.Channel) // counted on arrival: the rate the workers send at, whatever the answer

	// The delay of a real provider; ends early if the sender gives up.
	timer := time.NewTimer(p.cfg.Latency)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
	}

	if p.random() < p.cfg.FailureRate {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "simulated failure"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"messageId": uuid.NewString(),
		"status":    "accepted",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// count adds one received message of channel and updates its busiest one-second window.
func (p *Provider) count(channel string) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.received[channel]++
	p.stats.Received[channel]++

	// Keep only the arrivals of the last second, then this one: their number is the count of
	// the one-second window ending now. The busiest such window is the busiest of any window.
	recent := p.recent[channel]
	for len(recent) > 0 && now.Sub(recent[0]) >= time.Second {
		recent = recent[1:]
	}
	recent = append(recent, now)
	p.recent[channel] = recent
	p.stats.Busiest[channel] = max(p.stats.Busiest[channel], len(recent))
}

// serveStats answers GET with the measurements and resets them on DELETE.
func (p *Provider) serveStats(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, p.stats)
	case http.MethodDelete:
		p.stats = newStats()
		p.recent = map[string][]time.Time{}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET or DELETE"})
	}
}

// Report logs, once a second, how many messages per channel arrived in that second; quiet
// seconds are skipped. It returns when ctx ends.
func (p *Provider) Report(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		p.mu.Lock()
		counts := p.received
		p.received = map[string]int{}
		p.mu.Unlock()

		if len(counts) == 0 {
			continue
		}
		attrs := []any{}
		total := 0
		for ch, n := range counts {
			attrs = append(attrs, ch, n)
			total += n
		}
		p.logger.InfoContext(ctx, "received in the last second", append(attrs, "total", total)...)
	}
}

// writeJSON writes v as the JSON response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client went away; nothing left to do
}
