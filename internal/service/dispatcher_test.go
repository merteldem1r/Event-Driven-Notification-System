package service

import (
	"context"
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
	tried     []domain.Priority // every ClaimNext call, in order
	channels  []domain.Channel  // the channel of every ClaimNext call
	claimed   []domain.Priority // the successful ones
	lease     time.Duration
	results   []string // "sent:<messageId>", "failed:<error>" or "released"
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
	return domain.Notification{ID: uuid.New(), Channel: ch, Priority: p, Status: domain.StatusProcessing, Attempts: 1}, true, nil
}

func newTestDispatcher(q *fakeQueue) *Dispatcher {
	return NewDispatcher(domain.ChannelSMS, q, &fakeSender{}, time.Minute, slog.New(slog.DiscardHandler))
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
	d := NewDispatcher(domain.ChannelEmail, q, &fakeSender{}, time.Minute, slog.New(slog.DiscardHandler))

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
	found, err := NewDispatcher(domain.ChannelSMS, q, s, time.Minute, slog.New(slog.DiscardHandler)).ProcessNext(t.Context())
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

func TestDeliver_Failed(t *testing.T) {
	q := oneDue()
	s := &fakeSender{err: &provider.Error{StatusCode: 503, Err: errors.New("Service Unavailable")}}
	if _, err := NewDispatcher(domain.ChannelSMS, q, s, time.Minute, slog.New(slog.DiscardHandler)).ProcessNext(t.Context()); err != nil {
		t.Fatalf("ProcessNext: %v (a provider failure is recorded, not returned)", err)
	}
	if len(q.results) != 1 || q.results[0] != "failed:provider: status 503: Service Unavailable" {
		t.Errorf("results = %v, want failed with the error", q.results)
	}
}

// Shutdown cuts the send off: the notification goes back to the queue, and recording still works.
func TestDeliver_CutOffByShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	q := oneDue()
	s := &fakeSender{before: cancel, err: context.Canceled}

	if _, err := NewDispatcher(domain.ChannelSMS, q, s, time.Minute, slog.New(slog.DiscardHandler)).ProcessNext(ctx); err != nil {
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

	if _, err := NewDispatcher(domain.ChannelSMS, q, s, time.Minute, slog.New(slog.DiscardHandler)).ProcessNext(ctx); err != nil {
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
