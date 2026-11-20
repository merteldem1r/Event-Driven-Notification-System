package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// fakeQueue holds how many notifications are due per priority and records each claim.
type fakeQueue struct {
	due     map[domain.Priority]int
	err     error
	tried   []domain.Priority // every ClaimNext call, in order
	claimed []domain.Priority // the successful ones
	lease   time.Duration
}

func (q *fakeQueue) ClaimNext(_ context.Context, p domain.Priority, lease time.Duration) (domain.Notification, bool, error) {
	q.tried = append(q.tried, p)
	q.lease = lease
	if q.err != nil {
		return domain.Notification{}, false, q.err
	}
	if q.due[p] == 0 {
		return domain.Notification{}, false, nil
	}
	q.due[p]--
	q.claimed = append(q.claimed, p)
	return domain.Notification{ID: uuid.New(), Priority: p, Status: domain.StatusProcessing, Attempts: 1}, true, nil
}

func newTestDispatcher(q *fakeQueue) *Dispatcher {
	return NewDispatcher(q, time.Minute, slog.New(slog.DiscardHandler))
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
