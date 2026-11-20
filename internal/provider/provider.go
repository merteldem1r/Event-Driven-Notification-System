// Package provider sends notifications to the external provider over HTTP.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// maxResponseBytes caps how much of a response is read; the expected one is under 200 bytes.
const maxResponseBytes = 64 << 10

// maxIdleConns is how many open connections to the provider are kept for reuse. It must be at
// least the number of sends at once (WORKER_CONCURRENCY x 3 channels), or the rest reconnect
// for every message.
const maxIdleConns = 1000

// Message is one notification as the provider receives it.
type Message struct {
	ID            uuid.UUID // our notification ID, sent as Idempotency-Key
	CorrelationID string    // optional, sent as X-Correlation-ID
	To            string
	Channel       string
	Content       string
}

// Receipt is the provider's confirmation that it accepted a message.
type Receipt struct {
	MessageID  string
	Status     string
	AcceptedAt time.Time // zero if the provider's timestamp couldn't be read
}

// Error is a failed send. StatusCode is 0 when no response arrived (timeout, network error).
type Error struct {
	StatusCode int
	Err        error
}

// Error describes the failure without the response body, which may echo message content.
func (e *Error) Error() string {
	if e.StatusCode == 0 {
		return fmt.Sprintf("provider: no response: %v", e.Err)
	}
	return fmt.Sprintf("provider: status %d: %v", e.StatusCode, e.Err)
}

// Unwrap gives access to the cause, e.g. context.DeadlineExceeded.
func (e *Error) Unwrap() error { return e.Err }

// Temporary reports whether sending the same message again may succeed (D18): no response
// (timeout, network), 408, 429, 5xx, or a 2xx without a readable receipt. Everything else
// (3xx, other 4xx) means the provider rejected the request, and a retry would fail the same way.
func (e *Error) Temporary() bool {
	switch {
	case e.StatusCode == 0, e.StatusCode == http.StatusRequestTimeout, e.StatusCode == http.StatusTooManyRequests:
		return true
	case e.StatusCode >= 500:
		return true
	case e.StatusCode >= 200 && e.StatusCode <= 299: // accepted, but the receipt was unreadable
		return true
	default:
		return false
	}
}

// IsTemporary reports whether err is a provider error that a retry may fix. Any other error,
// such as a request that can't be built, is permanent.
func IsTemporary(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Temporary()
}

// Client sends messages to the provider's URL.
type Client struct {
	url     string
	timeout time.Duration
	http    *http.Client
}

// NewClient returns a Client that gives each send at most timeout.
func NewClient(url string, timeout time.Duration) *Client {
	// Go keeps only 2 idle connections per host by default; with many loops sending at once,
	// the others would open a new connection (and TLS handshake) for every message.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = maxIdleConns
	transport.MaxIdleConnsPerHost = maxIdleConns
	return &Client{url: url, timeout: timeout, http: &http.Client{Transport: transport}}
}

// request is the body the provider expects: {to, channel, content}.
type request struct {
	To      string `json:"to"`
	Channel string `json:"channel"`
	Content string `json:"content"`
}

// response is the body the provider returns: {messageId, status, timestamp}.
type response struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// Send posts the message and returns the provider's receipt, or an *Error.
func (c *Client) Send(ctx context.Context, m Message) (Receipt, error) {
	// A hanging provider must not hold a worker loop forever.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(request{To: m.To, Channel: m.Channel, Content: m.Content})
	if err != nil {
		return Receipt{}, fmt.Errorf("provider: encode request: %w", err)
	}
	// Not SSRF: the URL is PROVIDER_URL from the operator's config, never from a client request.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body)) //nolint:gosec // see above
	if err != nil {
		return Receipt{}, fmt.Errorf("provider: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Lets a provider recognise a retry of the same notification and not deliver it twice.
	req.Header.Set("Idempotency-Key", m.ID.String())
	if m.CorrelationID != "" {
		req.Header.Set("X-Correlation-ID", m.CorrelationID)
	}

	resp, err := c.http.Do(req) //nolint:gosec // URL from config, see above
	if err != nil {
		return Receipt{}, &Error{Err: err}
	}
	defer func() { _ = resp.Body.Close() }() // nothing useful to do if closing fails

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Receipt{}, &Error{StatusCode: resp.StatusCode, Err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Receipt{}, &Error{StatusCode: resp.StatusCode, Err: errors.New(http.StatusText(resp.StatusCode))}
	}

	var r response
	if err := json.Unmarshal(raw, &r); err != nil || r.MessageID == "" {
		return Receipt{}, &Error{StatusCode: resp.StatusCode, Err: errors.New("response has no messageId")}
	}
	receipt := Receipt{MessageID: r.MessageID, Status: r.Status}
	if t, err := time.Parse(time.RFC3339, r.Timestamp); err == nil { // informational; the messageId is what counts
		receipt.AcceptedAt = t
	}
	return receipt, nil
}
