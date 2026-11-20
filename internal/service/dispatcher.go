package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/provider"
)

// Queue hands out due notifications, one channel and priority at a time, and records what happened
// to them. The record methods take the claim's attempt number and report ok=false if it was claimed again since.
type Queue interface {
	ClaimNext(ctx context.Context, ch domain.Channel, p domain.Priority, lease time.Duration) (_ domain.Notification, ok bool, _ error)
	MarkSent(ctx context.Context, id uuid.UUID, attempt int, providerMessageID string) (ok bool, _ error)
	MarkFailed(ctx context.Context, id uuid.UUID, attempt int, lastError string) (ok bool, _ error)
	Release(ctx context.Context, id uuid.UUID, attempt int) (ok bool, _ error)
	Unclaim(ctx context.Context, id uuid.UUID, attempt int) (ok bool, _ error)
}

// Limiter hands out rate-limit slots per channel, shared by all workers (D15).
type Limiter interface {
	Wait(ctx context.Context, ch domain.Channel) error
}

// Sender delivers one message to the external provider.
type Sender interface {
	Send(ctx context.Context, m provider.Message) (provider.Receipt, error)
}

// recordTimeout bounds saving a send's result, which must happen even during shutdown.
const recordTimeout = 5 * time.Second

// claimPattern is D7, weighted priority: of every 10 claims of a channel, 6 prefer high, 3 normal
// and 1 low. Interleaved, so no priority waits through a long run of the others.
var claimPattern = [10]domain.Priority{
	domain.PriorityHigh, domain.PriorityNormal, domain.PriorityHigh, domain.PriorityHigh, domain.PriorityLow,
	domain.PriorityHigh, domain.PriorityNormal, domain.PriorityHigh, domain.PriorityNormal, domain.PriorityHigh,
}

// Dispatcher delivers the queued notifications of one channel, one per ProcessNext call; the
// worker calls it. Each channel has its own Dispatcher, so a backlog on one channel never
// holds up the others (D16).
type Dispatcher struct {
	channel domain.Channel
	queue   Queue
	sender  Sender
	limiter Limiter
	lease   time.Duration
	logger  *slog.Logger
	turn    atomic.Uint64 // position in claimPattern, shared by this channel's worker loops
}

// NewDispatcher returns a Dispatcher for channel ch that leases each claimed notification for
// lease, waits for a slot of limiter, and sends it through sender.
func NewDispatcher(ch domain.Channel, queue Queue, sender Sender, limiter Limiter, lease time.Duration, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{channel: ch, queue: queue, sender: sender, limiter: limiter, lease: lease, logger: logger}
}

// ProcessNext takes the next notification of the channel and delivers it; found is false if
// nothing is due.
func (d *Dispatcher) ProcessNext(ctx context.Context) (found bool, err error) {
	for _, p := range d.claimOrder() {
		n, ok, err := d.queue.ClaimNext(ctx, d.channel, p, d.lease)
		if err != nil {
			return false, fmt.Errorf("claim: %w", err)
		}
		if ok {
			return true, d.deliver(ctx, n)
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

// deliver waits for a rate-limit slot, sends a claimed notification and records the result:
// sent, failed, or back to pending if shutdown cut it off. A failure to record, or to get a
// slot, is returned.
func (d *Dispatcher) deliver(ctx context.Context, n domain.Notification) error {
	if n.CorrelationID != nil { // the API's ID, so one search finds the request and its delivery
		ctx = logging.WithCorrelationID(ctx, *n.CorrelationID)
	}
	// IDs only: never the recipient or the content.
	log := d.logger.With("notification_id", n.ID, "channel", n.Channel, "priority", n.Priority.String(), "attempt", n.Attempts)

	// The slot comes right before the send, so nothing slow (like a database pause) sits
	// between the permission and the request reaching the provider (D16).
	if err := d.limiter.Wait(ctx, d.channel); err != nil {
		return d.unclaim(ctx, log, n, err)
	}

	start := time.Now()
	receipt, sendErr := d.sender.Send(ctx, toMessage(n))
	took := time.Since(start).Milliseconds()

	// Record even if shutdown cancels ctx now: a send that isn't recorded as sent would be sent again.
	rec, cancel := recordContext(ctx)
	defer cancel()

	var recorded bool
	var err error
	switch {
	case sendErr == nil:
		recorded, err = d.queue.MarkSent(rec, n.ID, n.Attempts, receipt.MessageID)
		log.InfoContext(ctx, "notification sent", "provider_message_id", receipt.MessageID, "duration_ms", took)
	case ctx.Err() != nil: // we gave up at the shutdown deadline; the provider didn't fail
		recorded, err = d.queue.Release(rec, n.ID, n.Attempts)
		log.WarnContext(ctx, "send cut off by shutdown, notification back in the queue", "duration_ms", took)
	default:
		// Final for now; Phase 8 retries with backoff instead.
		recorded, err = d.queue.MarkFailed(rec, n.ID, n.Attempts, sendErr.Error())
		log.WarnContext(ctx, "notification failed", "error", sendErr, "duration_ms", took)
	}
	if err != nil {
		return fmt.Errorf("record result: %w", err)
	}
	if !recorded {
		log.WarnContext(ctx, "result not recorded: the notification was claimed again meanwhile")
	}
	return nil
}

// unclaim puts n back in the queue without counting the attempt, since nothing was sent: the
// rate limit couldn't be checked (e.g. Redis down: fail closed) or shutdown cut the wait off.
func (d *Dispatcher) unclaim(ctx context.Context, log *slog.Logger, n domain.Notification, waitErr error) error {
	rec, cancel := recordContext(ctx)
	defer cancel()
	recorded, err := d.queue.Unclaim(rec, n.ID, n.Attempts)
	if err != nil {
		return fmt.Errorf("put back after no rate-limit slot: %w", err)
	}
	if !recorded {
		log.WarnContext(ctx, "not put back: the notification was claimed again meanwhile")
	}
	if ctx.Err() != nil { // shutdown deadline, not a problem with the limiter
		log.WarnContext(ctx, "rate-limit wait cut off by shutdown, notification back in the queue")
		return nil
	}
	return waitErr // the worker logs it once per outage and waits before the next try
}

// recordContext is for saving a result: it survives shutdown, but not forever.
func recordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
}

// toMessage maps a notification to what the provider receives.
func toMessage(n domain.Notification) provider.Message {
	m := provider.Message{ID: n.ID, To: n.Recipient, Channel: string(n.Channel), Content: n.Content}
	if n.CorrelationID != nil {
		m.CorrelationID = *n.CorrelationID
	}
	return m
}
