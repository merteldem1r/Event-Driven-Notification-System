package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/provider"
)

// fakeQueue holds how many notifications are due per priority and records each claim and result.
type fakeQueue struct {
	due       map[domain.Priority]int
	err       error
	recordErr error
	stale     bool              // results report ok=false, as if claimed again meanwhile
	attempt   int               // attempt number of claimed notifications; 0 means 1
	createdAt time.Time         // created_at of claimed notifications
	scheduled *time.Time        // scheduled_at of claimed notifications
	tried     []domain.Priority // every ClaimNext call, in order
	channels  []domain.Channel  // the channel of every ClaimNext call
	claimed   []domain.Priority // the successful ones
	lease     time.Duration
	results   []string // "sent:<messageId>", "failed:<error>", "retry:<error> in <delay>", "released" or "unclaimed"
	recordCtx error    // ctx.Err() of the last record call
}

func (q *fakeQueue) record(ctx context.Context, result string) (bool, error) {
	q.recordCtx = ctx.Err()
	if q.recordErr != nil {
		return false, q.recordErr
	}
	q.results = append(q.results, result)
	return !q.stale, nil
}

func (q *fakeQueue) MarkSent(ctx context.Context, _ uuid.UUID, _ int, msgID string) (bool, error) {
	return q.record(ctx, "sent:"+msgID)
}

func (q *fakeQueue) MarkFailed(ctx context.Context, _ uuid.UUID, _ int, lastError string) (bool, error) {
	return q.record(ctx, "failed:"+lastError)
}

func (q *fakeQueue) Release(ctx context.Context, _ uuid.UUID, _ int) (bool, error) {
	return q.record(ctx, "released")
}

func (q *fakeQueue) Retry(ctx context.Context, _ uuid.UUID, _ int, lastError string, delay time.Duration) (bool, error) {
	return q.record(ctx, "retry:"+lastError+" in "+delay.String())
}

func (q *fakeQueue) Unclaim(ctx context.Context, _ uuid.UUID, _ int) (bool, error) {
	return q.record(ctx, "unclaimed")
}

// fakeSender answers every send with err, or a receipt; it keeps the last message.
type fakeSender struct {
	err    error
	before func() // runs during the send, e.g. to cancel the worker's context
	got    provider.Message
}

func (s *fakeSender) Send(_ context.Context, m provider.Message) (provider.Receipt, error) {
	s.got = m
	if s.before != nil {
		s.before()
	}
	if s.err != nil {
		return provider.Receipt{}, s.err
	}
	return provider.Receipt{MessageID: "msg-1", Status: "accepted"}, nil
}

func (q *fakeQueue) ClaimNext(_ context.Context, ch domain.Channel, p domain.Priority, lease time.Duration) (domain.Notification, bool, error) {
	q.tried = append(q.tried, p)
	q.channels = append(q.channels, ch)
	q.lease = lease
	if q.err != nil {
		return domain.Notification{}, false, q.err
	}
	if q.due[p] == 0 {
		return domain.Notification{}, false, nil
	}
	q.due[p]--
	q.claimed = append(q.claimed, p)
	return domain.Notification{ID: uuid.New(), Channel: ch, Priority: p, Status: domain.StatusProcessing, Attempts: max(q.attempt, 1), CreatedAt: q.createdAt, ScheduledAt: q.scheduled}, true, nil
}

// fakeRecorder keeps what the dispatcher reported, as "<channel>:<result>" and delivery times.
type fakeRecorder struct {
	sends     []string
	delivered []time.Duration
}

func (r *fakeRecorder) SendDone(ch domain.Channel, result domain.SendResult, _ time.Duration) {
	r.sends = append(r.sends, string(ch)+":"+string(result))
}

func (r *fakeRecorder) Delivered(_ domain.Channel, since time.Duration) {
	r.delivered = append(r.delivered, since)
}

// fakeRetries allows max attempts and waits one second per attempt plus the provider's
// Retry-After, so tests can see both reached it.
type fakeRetries struct{ max int }

func (r fakeRetries) Next(attempt int, retryAfter time.Duration) (time.Duration, bool) {
	if attempt >= r.max {
		return 0, false
	}
	return time.Duration(attempt)*time.Second + retryAfter, true
}

// fakeLimiter answers every Wait with err (or, with ctxErr, the context's error) and records
// the channels asked for. before runs first, e.g. to cancel the worker's context.
type fakeLimiter struct {
	err      error
	ctxErr   bool
	before   func()
	channels []domain.Channel
}

func (l *fakeLimiter) Wait(ctx context.Context, ch domain.Channel) error {
	l.channels = append(l.channels, ch)
	if l.before != nil {
		l.before()
	}
	if l.ctxErr {
		return ctx.Err()
	}
	return l.err
}

func newTestDispatcher(q *fakeQueue) *Dispatcher {
	return NewDispatcher(domain.ChannelSMS, q, &fakeSender{}, &fakeLimiter{}, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler))
}

// count tallies claims per priority.
func count(ps []domain.Priority) map[domain.Priority]int {
	m := map[domain.Priority]int{}
	for _, p := range ps {
		m[p]++
	}
	return m
}

func TestProcessNext_WeightsUnderFullLoad(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{
		domain.PriorityHigh: 1000, domain.PriorityNormal: 1000, domain.PriorityLow: 1000,
	}}
	d := newTestDispatcher(q)

	for range 100 {
		if found, err := d.ProcessNext(t.Context()); !found || err != nil {
			t.Fatalf("ProcessNext = %v, %v; want a claim", found, err)
		}
	}
	got := count(q.claimed)
	if got[domain.PriorityHigh] != 60 || got[domain.PriorityNormal] != 30 || got[domain.PriorityLow] != 10 {
		t.Errorf("claims per priority = %v, want 60 high, 30 normal, 10 low (D7)", got)
	}
	if q.lease != time.Minute {
		t.Errorf("lease = %v, want the configured 1m", q.lease)
	}
}

// Low is never starved: in every 10 claims it gets one, however much high is waiting (IMP-6).
func TestProcessNext_LowIsNotStarved(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{domain.PriorityHigh: 1_000_000, domain.PriorityLow: 1}}
	d := newTestDispatcher(q)

	for range 10 {
		if _, err := d.ProcessNext(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(q.claimed)[domain.PriorityLow]; got != 1 {
		t.Errorf("low claims in 10 turns = %d, want 1", got)
	}
}

// An empty preferred queue falls back, so all capacity goes to whatever is waiting.
func TestProcessNext_FallsBackWhenPreferredIsEmpty(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{domain.PriorityLow: 10}}
	d := newTestDispatcher(q)

	for range 10 {
		if found, err := d.ProcessNext(t.Context()); !found || err != nil {
			t.Fatalf("ProcessNext = %v, %v; want a low claim", found, err)
		}
	}
	if got := count(q.claimed)[domain.PriorityLow]; got != 10 {
		t.Errorf("low claims = %d, want all 10", got)
	}
}

// A dispatcher only ever claims its own channel (D16).
func TestProcessNext_ClaimsOnlyItsChannel(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{domain.PriorityLow: 1}}
	d := NewDispatcher(domain.ChannelEmail, q, &fakeSender{}, &fakeLimiter{}, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler))

	for range 2 { // one claim, then an empty queue
		if _, err := d.ProcessNext(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range q.channels {
		if ch != domain.ChannelEmail {
			t.Fatalf("claimed channels %v, want only email", q.channels)
		}
	}
	if len(q.channels) == 0 {
		t.Fatal("no claims made")
	}
}

// The slot is asked for after the claim, right before the send, for the dispatcher's own channel (D16).
func TestProcessNext_WaitsForASlotBeforeSending(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{domain.PriorityNormal: 1}}
	s := &fakeSender{}
	l := &fakeLimiter{before: func() {
		if s.got.ID != uuid.Nil {
			t.Error("sent before the rate-limit slot was given")
		}
	}}
	d := NewDispatcher(domain.ChannelPush, q, s, l, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler))

	if found, err := d.ProcessNext(t.Context()); !found || err != nil {
		t.Fatalf("ProcessNext = %v, %v; want a delivery", found, err)
	}
	if len(l.channels) != 1 || l.channels[0] != domain.ChannelPush {
		t.Errorf("limiter asked for %v, want [push]", l.channels)
	}
	if len(q.results) != 1 || q.results[0] != "sent:msg-1" {
		t.Errorf("results = %v, want [sent:msg-1]", q.results)
	}
}

// With an empty queue nothing is claimed, so no slot is taken (idle loops use none).
func TestProcessNext_EmptyQueueTakesNoSlot(t *testing.T) {
	l := &fakeLimiter{}
	d := NewDispatcher(domain.ChannelSMS, &fakeQueue{due: map[domain.Priority]int{}}, &fakeSender{}, l, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler))
	if _, err := d.ProcessNext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(l.channels) != 0 {
		t.Errorf("limiter asked %d times, want 0", len(l.channels))
	}
}

// No slot (e.g. Redis down): nothing is sent, the notification goes back without counting
// the attempt (fail closed), and the error reaches the worker so it waits before retrying.
func TestProcessNext_NoSlotPutsBackWithoutSending(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{domain.PriorityNormal: 1}}
	s := &fakeSender{}
	redisDown := errors.New("rate limit sms: connection refused")
	d := NewDispatcher(domain.ChannelSMS, q, s, &fakeLimiter{err: redisDown}, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler))

	found, err := d.ProcessNext(t.Context())
	if !found || !errors.Is(err, redisDown) {
		t.Fatalf("ProcessNext = %v, %v; want found and the limiter's error", found, err)
	}
	if s.got.ID != uuid.Nil {
		t.Error("sent without a rate-limit slot")
	}
	if len(q.results) != 1 || q.results[0] != "unclaimed" {
		t.Errorf("results = %v, want [unclaimed]", q.results)
	}
}

// Shutdown's deadline during the wait: put back without counting the attempt, and no error,
// since the limiter didn't fail. The put-back is saved even though the context is cancelled.
func TestProcessNext_ShutdownDuringWaitPutsBack(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{domain.PriorityNormal: 1}}
	ctx, cancel := context.WithCancel(t.Context())
	l := &fakeLimiter{before: cancel, ctxErr: true}
	d := NewDispatcher(domain.ChannelSMS, q, &fakeSender{}, l, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler))

	if _, err := d.ProcessNext(ctx); err != nil {
		t.Fatalf("ProcessNext = %v, want nil", err)
	}
	if len(q.results) != 1 || q.results[0] != "unclaimed" || q.recordCtx != nil {
		t.Errorf("results = %v (record ctx error %v), want [unclaimed] saved with a live context", q.results, q.recordCtx)
	}
}

func TestProcessNext_EmptyQueue(t *testing.T) {
	q := &fakeQueue{due: map[domain.Priority]int{}}
	found, err := newTestDispatcher(q).ProcessNext(t.Context())
	if found || err != nil {
		t.Errorf("ProcessNext = %v, %v; want nothing found", found, err)
	}
	if len(q.tried) != 3 {
		t.Errorf("tried %v, want each priority once", q.tried)
	}
}

func TestProcessNext_ClaimError(t *testing.T) {
	boom := errors.New("connection refused")
	q := &fakeQueue{err: boom}
	found, err := newTestDispatcher(q).ProcessNext(t.Context())
	if found || !errors.Is(err, boom) {
		t.Errorf("ProcessNext = %v, %v; want the wrapped claim error", found, err)
	}
	if len(q.tried) != 1 {
		t.Errorf("tried %d priorities, want to stop at the first error", len(q.tried))
	}
}

// oneDue is a queue with a single due notification.
func oneDue() *fakeQueue {
	return &fakeQueue{due: map[domain.Priority]int{domain.PriorityNormal: 1}}
}

func TestDeliver_Sent(t *testing.T) {
	q, s := oneDue(), &fakeSender{}
	found, err := NewDispatcher(domain.ChannelSMS, q, s, &fakeLimiter{}, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler)).ProcessNext(t.Context())
	if !found || err != nil {
		t.Fatalf("ProcessNext = %v, %v", found, err)
	}
	if len(q.results) != 1 || q.results[0] != "sent:msg-1" {
		t.Errorf("results = %v, want sent with the provider's message ID", q.results)
	}
}

func TestDeliver_MapsTheNotification(t *testing.T) {
	id := uuid.New()
	n := domain.Notification{
		ID: id, CorrelationID: ptr("order-1042"), Channel: domain.ChannelSMS,
		Recipient: "+905551234567", Content: "hi",
	}
	want := provider.Message{ID: id, CorrelationID: "order-1042", To: "+905551234567", Channel: "sms", Content: "hi"}
	if got := toMessage(n); got != want {
		t.Errorf("toMessage = %+v, want %+v", got, want)
	}
	if got := toMessage(domain.Notification{ID: id}); got.CorrelationID != "" {
		t.Errorf("without a correlation ID: %q, want empty", got.CorrelationID)
	}
}

// A temporary failure goes back to the queue for later, with the wait from the policy and the
// provider's Retry-After, while attempts are left; a permanent one, or the last attempt, is final.
func TestDeliver_Failed(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		attempt    int
		wantResult string
		wantLog    string
	}{
		{"provider down: retry", &provider.Error{StatusCode: 503, Err: errors.New("Service Unavailable")}, 1,
			"retry:provider: status 503: Service Unavailable in 1s", "notification failed, will retry"},
		{"too many requests: Retry-After reaches the backoff",
			&provider.Error{StatusCode: 429, RetryAfter: 30 * time.Second, Err: errors.New("Too Many Requests")}, 1,
			"retry:provider: status 429: Too Many Requests in 31s", "notification failed, will retry"},
		{"4th attempt: one more left", &provider.Error{StatusCode: 503, Err: errors.New("Service Unavailable")}, 4,
			"retry:provider: status 503: Service Unavailable in 4s", "notification failed, will retry"},
		{"5th attempt: none left, final with the last error",
			&provider.Error{StatusCode: 503, Err: errors.New("Service Unavailable")}, 5,
			"failed:provider: status 503: Service Unavailable", "notification failed"},
		{"rejected: final", &provider.Error{StatusCode: 400, Err: errors.New("Bad Request")}, 1,
			"failed:provider: status 400: Bad Request", "notification failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := oneDue()
			q.attempt = tt.attempt
			var logs bytes.Buffer
			d := NewDispatcher(domain.ChannelSMS, q, &fakeSender{err: tt.err}, &fakeLimiter{}, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute,
				slog.New(slog.NewJSONHandler(&logs, nil)))
			if _, err := d.ProcessNext(t.Context()); err != nil {
				t.Fatalf("ProcessNext: %v (a provider failure is recorded, not returned)", err)
			}
			if len(q.results) != 1 || q.results[0] != tt.wantResult {
				t.Errorf("results = %v, want [%s]", q.results, tt.wantResult)
			}
			var line struct {
				Msg string `json:"msg"`
			}
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil || line.Msg != tt.wantLog {
				t.Errorf("log = %s (%v), want %q", logs.String(), err, tt.wantLog)
			}
		})
	}
}

// Shutdown cuts the send off: the notification goes back to the queue, and recording still works.
func TestDeliver_CutOffByShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	q := oneDue()
	s := &fakeSender{before: cancel, err: context.Canceled}

	if _, err := NewDispatcher(domain.ChannelSMS, q, s, &fakeLimiter{}, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler)).ProcessNext(ctx); err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	if len(q.results) != 1 || q.results[0] != "released" {
		t.Errorf("results = %v, want released", q.results)
	}
	if q.recordCtx != nil {
		t.Errorf("recorded with a cancelled context (%v); the result would be lost", q.recordCtx)
	}
}

// A successful send is recorded even if shutdown cancels the context right after it.
func TestDeliver_RecordsSentDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	q := oneDue()
	s := &fakeSender{before: cancel} // cancelled, but the provider still answered 202

	if _, err := NewDispatcher(domain.ChannelSMS, q, s, &fakeLimiter{}, fakeRetries{max: 5}, &fakeRecorder{}, time.Minute, slog.New(slog.DiscardHandler)).ProcessNext(ctx); err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	if len(q.results) != 1 || q.results[0] != "sent:msg-1" || q.recordCtx != nil {
		t.Errorf("results = %v (record ctx err %v), want sent, recorded with a live context", q.results, q.recordCtx)
	}
}

func TestDeliver_RecordError(t *testing.T) {
	boom := errors.New("database down")
	q := oneDue()
	q.recordErr = boom
	found, err := newTestDispatcher(q).ProcessNext(t.Context())
	if !found || !errors.Is(err, boom) {
		t.Errorf("ProcessNext = %v, %v; want found and the record error", found, err)
	}
}

func TestDeliver_ClaimedAgainMeanwhile(t *testing.T) {
	q := oneDue()
	q.stale = true
	if found, err := newTestDispatcher(q).ProcessNext(t.Context()); !found || err != nil {
		t.Errorf("ProcessNext = %v, %v; want found, no error (it is only logged)", found, err)
	}
}

// Every send is reported once with how it ended; a delivery also with its time since creation.
func TestDeliver_ReportsMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		sender  *fakeSender
		attempt int
		want    string
	}{
		{"sent", t.Context(), &fakeSender{}, 1, "sms:sent"},
		{"temporary, attempts left", t.Context(), &fakeSender{err: &provider.Error{StatusCode: 503, Err: errors.New("down")}}, 1, "sms:retry"},
		{"temporary, last attempt", t.Context(), &fakeSender{err: &provider.Error{StatusCode: 503, Err: errors.New("down")}}, 5, "sms:failed"},
		{"rejected", t.Context(), &fakeSender{err: &provider.Error{StatusCode: 400, Err: errors.New("bad")}}, 1, "sms:failed"},
		{"cut off by shutdown", ctx, &fakeSender{before: cancel, err: context.Canceled}, 1, "sms:interrupted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := oneDue()
			q.attempt, q.createdAt = tt.attempt, time.Now().Add(-3*time.Second)
			m := &fakeRecorder{}
			d := NewDispatcher(domain.ChannelSMS, q, tt.sender, &fakeLimiter{}, fakeRetries{max: 5}, m, time.Minute,
				slog.New(slog.DiscardHandler))
			if _, err := d.ProcessNext(tt.ctx); err != nil {
				t.Fatal(err)
			}
			if len(m.sends) != 1 || m.sends[0] != tt.want {
				t.Errorf("reported %v, want [%s]", m.sends, tt.want)
			}
			wantDelivered := tt.want == "sms:sent"
			if (len(m.delivered) == 1) != wantDelivered {
				t.Fatalf("delivered = %v, want one only when sent", m.delivered)
			}
			if wantDelivered && (m.delivered[0] < 3*time.Second || m.delivered[0] > 4*time.Second) {
				t.Errorf("delivered after %v, want about 3s since creation", m.delivered[0])
			}
		})
	}
}

// A scheduled notification's delivery time counts from its scheduled time, not its creation.
func TestDeliver_DeliveryTimeFromScheduledTime(t *testing.T) {
	q := oneDue()
	q.createdAt = time.Now().Add(-time.Hour)            // created an hour ago
	q.scheduled = ptr(time.Now().Add(-2 * time.Second)) // for two seconds ago
	m := &fakeRecorder{}
	d := NewDispatcher(domain.ChannelSMS, q, &fakeSender{}, &fakeLimiter{}, fakeRetries{max: 5}, m, time.Minute,
		slog.New(slog.DiscardHandler))
	if _, err := d.ProcessNext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(m.delivered) != 1 || m.delivered[0] < 2*time.Second || m.delivered[0] > 3*time.Second {
		t.Errorf("delivered = %v, want about 2s since the scheduled time", m.delivered)
	}
}
