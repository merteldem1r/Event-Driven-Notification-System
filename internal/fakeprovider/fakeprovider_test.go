package fakeprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/provider"
)

func newTestProvider(cfg Config) *Provider {
	return New(cfg, slog.New(slog.DiscardHandler))
}

// The worker's own provider client accepts what the fake answers, as it does webhook.site's.
func TestWorkerClientAcceptsTheAnswer(t *testing.T) {
	srv := httptest.NewServer(newTestProvider(Config{}))
	defer srv.Close()

	receipt, err := provider.NewClient(srv.URL+"/any-path", time.Second).Send(t.Context(), provider.Message{
		ID: uuid.New(), To: "+905551234567", Channel: "sms", Content: "hello",
	})
	if err != nil {
		t.Fatalf("Send = %v, want a receipt", err)
	}
	if _, err := uuid.Parse(receipt.MessageID); err != nil || receipt.Status != "accepted" || receipt.AcceptedAt.IsZero() {
		t.Errorf("receipt = %+v, want a UUID messageId, status accepted and a timestamp", receipt)
	}
}

// With a failure rate of 1, every send fails with 503, as a provider outage would.
func TestFailureRate(t *testing.T) {
	srv := httptest.NewServer(newTestProvider(Config{FailureRate: 1}))
	defer srv.Close()

	_, err := provider.NewClient(srv.URL, time.Second).Send(t.Context(), provider.Message{
		ID: uuid.New(), To: "a@example.com", Channel: "email", Content: "hello",
	})
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Send error = %v, want a provider error with status 503", err)
	}
}

// Requests fail where the random number falls below the rate: 0.3 fails 0.1 and 0.2, not 0.5.
func TestFailureRate_Partial(t *testing.T) {
	p := newTestProvider(Config{FailureRate: 0.3})
	var mu sync.Mutex
	next := []float64{0.1, 0.5, 0.2}
	p.random = func() float64 {
		mu.Lock()
		defer mu.Unlock()
		v := next[0]
		next = next[1:]
		return v
	}

	var got []int
	for range 3 {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"to":"a","channel":"sms","content":"c"}`)))
		got = append(got, w.Code)
	}
	want := []int{503, 202, 503}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statuses = %v, want %v", got, want)
		}
	}
}

func TestBadRequests(t *testing.T) {
	tests := []struct {
		name, method, body string
		want               int
	}{
		{"not JSON", http.MethodPost, `hello`, http.StatusBadRequest},
		{"missing content", http.MethodPost, `{"to":"a","channel":"sms"}`, http.StatusBadRequest},
		{"wrong method", http.MethodGet, ``, http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			newTestProvider(Config{}).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), tt.method, "/", strings.NewReader(tt.body)))
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

// Each answer takes the configured latency (virtual time, so the test doesn't wait).
func TestLatency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestProvider(Config{Latency: 2 * time.Second})
		start := time.Now()
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"to":"a","channel":"sms","content":"c"}`)))
		if took := time.Since(start); took != 2*time.Second || w.Code != http.StatusAccepted {
			t.Errorf("took %v with status %d, want 2s and 202", took, w.Code)
		}
	})
}

// syncBuffer collects log output from two goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Once a second, the counts per channel of that second are logged; quiet seconds log nothing.
func TestReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs syncBuffer
		p := New(Config{}, slog.New(slog.NewJSONHandler(&logs, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go p.Report(ctx)

		for _, ch := range []string{"sms", "sms", "sms", "email"} {
			body := `{"to":"a","channel":"` + ch + `","content":"c"}`
			p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body)))
		}
		time.Sleep(time.Second) // first report
		synctest.Wait()
		time.Sleep(time.Second) // a quiet second: no line
		synctest.Wait()

		lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
		if len(lines) != 1 {
			t.Fatalf("log lines = %q, want exactly one report", lines)
		}
		var report map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &report); err != nil {
			t.Fatal(err)
		}
		if report["sms"] != 3.0 || report["email"] != 1.0 || report["total"] != 4.0 {
			t.Errorf("report = %v, want sms 3, email 1, total 4", report)
		}
	})
}
