package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

const accepted = `{"messageId":"3f1b6a6e-1d4c-4c39-9a8e-2b7d9c0e5f11","status":"accepted","timestamp":"2026-11-20T10:00:00Z"}`

var testMessage = Message{
	ID: uuid.MustParse("7c9e6679-7425-40de-944b-e07fc1f90ae7"), CorrelationID: "order-1042",
	To: "+905551234567", Channel: "sms", Content: "Your code is 4821",
}

// serve starts a provider stand-in that answers with status and body.
func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body) // the client side is what's under test
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSend_Accepted(t *testing.T) {
	var got *http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, accepted)
	}))
	defer srv.Close()

	receipt, err := NewClient(srv.URL, time.Second).Send(t.Context(), testMessage)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	want := Receipt{
		MessageID: "3f1b6a6e-1d4c-4c39-9a8e-2b7d9c0e5f11", Status: "accepted",
		AcceptedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
	if receipt != want {
		t.Errorf("receipt = %+v, want %+v", receipt, want)
	}

	// The request is exactly the provider's format.
	if got.Method != http.MethodPost || got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request: %s with Content-Type %q, want POST application/json", got.Method, got.Header.Get("Content-Type"))
	}
	if body := string(gotBody); body != `{"to":"+905551234567","channel":"sms","content":"Your code is 4821"}` {
		t.Errorf("body = %s", body)
	}
	if k := got.Header.Get("Idempotency-Key"); k != testMessage.ID.String() {
		t.Errorf("Idempotency-Key = %q, want the notification ID", k)
	}
	if c := got.Header.Get("X-Correlation-ID"); c != "order-1042" {
		t.Errorf("X-Correlation-ID = %q, want order-1042", c)
	}
}

// Many sends at once reuse their connections instead of opening a new one per message.
func TestSend_ReusesConnections(t *testing.T) {
	var opened atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, accepted) // the client side is what's under test
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	const senders, each = 30, 20 // like 30 worker loops
	client := NewClient(srv.URL, time.Second)
	var wg sync.WaitGroup
	for range senders {
		wg.Go(func() {
			for range each {
				if _, err := client.Send(t.Context(), testMessage); err != nil {
					t.Errorf("Send: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	// About one per sender (Go may open a few extra when a connection frees up while it dials);
	// with Go's default of 2 kept per host it was 175 to 238.
	if n := opened.Load(); n > 2*senders {
		t.Errorf("%d connections opened for %d sends, want at most %d", n, senders*each, 2*senders)
	}
}

func TestSend_WithoutCorrelationID(t *testing.T) {
	var header []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Values("X-Correlation-ID")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, accepted)
	}))
	defer srv.Close()

	m := testMessage
	m.CorrelationID = ""
	if _, err := NewClient(srv.URL, time.Second).Send(t.Context(), m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if header != nil {
		t.Errorf("X-Correlation-ID sent as %q, want no header", header)
	}
}

// Every failed answer is an *Error with its status, classified as temporary or not (D18).
func TestSend_Failures(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantTemporary bool
	}{
		{"server error", http.StatusInternalServerError, "", true},
		{"bad gateway", http.StatusBadGateway, "", true},
		{"provider down", http.StatusServiceUnavailable, "", true},
		{"too many requests", http.StatusTooManyRequests, "", true},
		{"request timeout", http.StatusRequestTimeout, "", true},
		{"accepted without messageId", http.StatusAccepted, `{"status":"accepted"}`, true},
		{"accepted with non-JSON body", http.StatusAccepted, "OK", true},
		{"bad request", http.StatusBadRequest, `{"error":"bad"}`, false},
		{"unauthorized", http.StatusUnauthorized, "", false},
		{"not found", http.StatusNotFound, "", false},
		{"unprocessable", http.StatusUnprocessableEntity, "", false},
		{"redirect not followed to a 2xx", http.StatusNotModified, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serve(t, tt.status, tt.body)
			_, err := NewClient(srv.URL, time.Second).Send(t.Context(), testMessage)
			var pe *Error
			if !errors.As(err, &pe) || pe.StatusCode != tt.status {
				t.Fatalf("err = %v, want a provider *Error with status %d", err, tt.status)
			}
			if got := IsTemporary(err); got != tt.wantTemporary {
				t.Errorf("IsTemporary = %v, want %v", got, tt.wantTemporary)
			}
		})
	}
}

// Only provider errors can be temporary, also when wrapped; our own errors are permanent.
func TestIsTemporary(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"wrapped provider error", fmt.Errorf("send: %w", &Error{StatusCode: 503, Err: errors.New("down")}), true},
		{"other error", errors.New("provider: encode request: bad"), false},
		{"no error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTemporary(tt.err); got != tt.want {
				t.Errorf("IsTemporary(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// A provider URL that can't be used is our mistake: retrying can't fix it.
func TestSend_BadURLIsPermanent(t *testing.T) {
	_, err := NewClient("http://bad host/", time.Second).Send(t.Context(), testMessage)
	if err == nil || IsTemporary(err) {
		t.Errorf("err = %v (temporary %v), want a permanent error", err, IsTemporary(err))
	}
}

// The error text goes to logs, so it must never contain the message content.
func TestSend_ErrorDoesNotLeakContent(t *testing.T) {
	srv := serve(t, http.StatusBadRequest, `{"error":"invalid content: Your code is 4821"}`)
	_, err := NewClient(srv.URL, time.Second).Send(t.Context(), testMessage)
	if err == nil || strings.Contains(err.Error(), "4821") {
		t.Errorf("err = %v; want an error without the content", err)
	}
}

func TestSend_Timeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release // a provider that never answers
	}))
	defer srv.Close()
	defer close(release) // runs first: lets the handler return so Close doesn't wait forever

	start := time.Now()
	_, err := NewClient(srv.URL, 50*time.Millisecond).Send(t.Context(), testMessage)
	var pe *Error
	if !errors.As(err, &pe) || pe.StatusCode != 0 || !errors.Is(err, context.DeadlineExceeded) || !IsTemporary(err) {
		t.Errorf("err = %v, want a temporary provider *Error without status, caused by the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Send took %v, want about the 50ms timeout", elapsed)
	}
}

func TestSend_NoConnection(t *testing.T) {
	srv := serve(t, http.StatusAccepted, accepted)
	url := srv.URL
	srv.Close() // nothing listens there any more

	_, err := NewClient(url, time.Second).Send(t.Context(), testMessage)
	var pe *Error
	if !errors.As(err, &pe) || pe.StatusCode != 0 || !IsTemporary(err) {
		t.Errorf("err = %v, want a temporary provider *Error without status", err)
	}
}

// TestSend_WebhookSite sends one message to a real webhook.site URL, configured to answer 202
// with {messageId, status, timestamp}. Skipped unless PROVIDER_TEST_URL is set:
//
//	PROVIDER_TEST_URL=https://webhook.site/<your-id> go test ./internal/provider -run WebhookSite -v
func TestSend_WebhookSite(t *testing.T) {
	url := os.Getenv("PROVIDER_TEST_URL")
	if url == "" {
		t.Skip("set PROVIDER_TEST_URL to your webhook.site URL to run this test")
	}
	m := testMessage
	m.ID, m.CorrelationID, m.Content = uuid.New(), "provider-test", "Hello from the provider test"

	receipt, err := NewClient(url, 10*time.Second).Send(t.Context(), m)
	if err != nil {
		t.Fatalf("Send: %v (is the URL's response set to 202 with a messageId?)", err)
	}
	out, _ := json.Marshal(receipt)
	t.Logf("receipt: %s; look for Idempotency-Key %s on webhook.site", out, m.ID)
}
