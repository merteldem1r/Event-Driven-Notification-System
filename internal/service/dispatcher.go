package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
)

// Queue hands out due notifications, one priority at a time.
type Queue interface {
	ClaimNext(ctx context.Context, p domain.Priority, lease time.Duration) (_ domain.Notification, ok bool, _ error)
}

// claimPattern is D7, weighted priority: of every 10 claims, 6 prefer high, 3 normal and
// 1 low. Interleaved, so no priority waits through a long run of the others.
var claimPattern = [10]domain.Priority{
	domain.PriorityHigh, domain.PriorityNormal, domain.PriorityHigh, domain.PriorityHigh, domain.PriorityLow,
	domain.PriorityHigh, domain.PriorityNormal, domain.PriorityHigh, domain.PriorityNormal, domain.PriorityHigh,
}

// Dispatcher delivers queued notifications, one per ProcessNext call; the worker calls it.
type Dispatcher struct {
	queue  Queue
	lease  time.Duration
	logger *slog.Logger
	turn   atomic.Uint64 // position in claimPattern, shared by all worker loops
}

// NewDispatcher returns a Dispatcher that leases each claimed notification for lease.
func NewDispatcher(queue Queue, lease time.Duration, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{queue: queue, lease: lease, logger: logger}
}

// ProcessNext takes the next notification and delivers it; found is false if nothing is due.
func (d *Dispatcher) ProcessNext(ctx context.Context) (found bool, err error) {
	for _, p := range d.claimOrder() {
		n, ok, err := d.queue.ClaimNext(ctx, p, d.lease)
		if err != nil {
			return false, fmt.Errorf("claim: %w", err)
		}
		if ok {
			d.deliver(ctx, n)
			return true, nil
		}
	}
	return false, nil
}

// claimOrder is the preferred priority for this turn, then the others from high to low,
// so an empty preferred queue never wastes the claim.
func (d *Dispatcher) claimOrder() []domain.Priority {
	preferred := claimPattern[(d.turn.Add(1)-1)%uint64(len(claimPattern))]
	order := []domain.Priority{preferred}
	for _, p := range []domain.Priority{domain.PriorityHigh, domain.PriorityNormal, domain.PriorityLow} {
		if p != preferred {
			order = append(order, p)
		}
	}
	return order
}

// deliver handles a claimed notification. Sending it and recording the result are added in
// steps 6.3 and 6.4; until then it stays processing.
func (d *Dispatcher) deliver(ctx context.Context, n domain.Notification) {
	if n.CorrelationID != nil { // the API's ID, so one search finds the request and its delivery
		ctx = logging.WithCorrelationID(ctx, *n.CorrelationID)
	}
	d.logger.InfoContext(ctx, "notification claimed",
		"notification_id", n.ID, "channel", n.Channel, "priority", n.Priority.String(), "attempt", n.Attempts)
}
