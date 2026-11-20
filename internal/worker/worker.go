// Package worker runs the loops that take notifications from the queue and process them,
// and stops them gracefully.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrShutdownTimeout means in-flight work didn't finish in time and was cancelled.
var ErrShutdownTimeout = errors.New("shutdown timeout: in-flight work was cancelled")

// Processor handles one queued notification per call.
type Processor interface {
	// Wait blocks until the next notification may be taken, e.g. until a rate-limit slot is
	// free. It returns an error if ctx ends first or permission can't be checked.
	Wait(ctx context.Context) error
	// ProcessNext takes the next notification from the queue and handles it.
	// found is false when the queue had nothing to take.
	ProcessNext(ctx context.Context) (found bool, err error)
}

// Config controls the loops.
type Config struct {
	Concurrency     int           // loops running in parallel for each processor
	PollInterval    time.Duration // wait after an empty queue or an error
	ShutdownTimeout time.Duration // time in-flight work gets after Run's context is cancelled
}

// Worker runs Config.Concurrency loops for each processor, each loop calling its processor
// again and again. The worker program has one processor per channel (D16).
type Worker struct {
	procs   []Processor
	cfg     Config
	logger  *slog.Logger
	trouble trouble
}

// trouble tracks errors in a row across all loops, so an outage (e.g. Redis or the database
// down) is logged when it starts and when it ends, not on every retry of every loop.
type trouble struct {
	mu       sync.Mutex
	failing  int       // loops whose last try failed
	since    time.Time // when the first of them failed
	failures int       // failed tries since then
}

// New returns a Worker. cfg is expected to be valid (config.Load checks it).
func New(procs []Processor, cfg Config, logger *slog.Logger) *Worker {
	return &Worker{procs: procs, cfg: cfg, logger: logger}
}

// Run processes notifications until ctx is cancelled, then shuts down gracefully: no new
// notification is taken, and those in progress get up to ShutdownTimeout to finish.
// It returns ErrShutdownTimeout if they had to be cancelled.
func (w *Worker) Run(ctx context.Context) error {
	// In-flight work must not stop with the signal, only at the shutdown deadline.
	work, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()

	var wg sync.WaitGroup
	for p, proc := range w.procs {
		for i := range w.cfg.Concurrency {
			id := p*w.cfg.Concurrency + i // unique across processors, for the logs
			wg.Go(func() { w.loop(ctx, work, proc, id) })
		}
	}
	w.logger.Info("worker started", "processors", len(w.procs), "loops_per_processor", w.cfg.Concurrency,
		"poll_interval", w.cfg.PollInterval.String()) // "500ms", not nanoseconds

	<-ctx.Done()
	w.logger.Info("worker stopping: finishing in-flight work", "timeout", w.cfg.ShutdownTimeout.String())

	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()

	timer := time.NewTimer(w.cfg.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
		w.logger.Info("worker stopped")
		return nil
	case <-timer.C:
		cancelWork() // processors see ctx.Err() and give up
		<-stopped
		return ErrShutdownTimeout
	}
}

// loop has proc process one notification after another until stop is cancelled.
// stop decides whether to take more work; work is passed to the processor.
func (w *Worker) loop(stop, work context.Context, proc Processor, id int) {
	failing := false // this loop's last try failed
	for stop.Err() == nil {
		// Nothing is taken while waiting, so the stop signal ends the wait right away.
		err := proc.Wait(stop)
		if stop.Err() != nil {
			return // stopped while waiting: take nothing new
		}
		if err != nil { // e.g. Redis down: take nothing (fail closed) and try again later
			w.failed(work, &failing, id, "waiting to process failed", err)
			w.pause(stop)
			continue
		}

		found, err := proc.ProcessNext(work)
		if err != nil { // e.g. database down: wait, so we don't spin
			w.failed(work, &failing, id, "processing failed", err)
			w.pause(stop)
			continue
		}
		w.succeeded(work, &failing)
		if !found {
			w.pause(stop) // empty queue; if found, more may be waiting, so go on at once
		}
	}
}

// failed records a failed try of one loop. Only the first failure of an outage is logged as
// an error; the retries that follow are logged at debug level.
func (w *Worker) failed(ctx context.Context, failing *bool, id int, msg string, err error) {
	t := &w.trouble
	t.mu.Lock()
	defer t.mu.Unlock()
	if !*failing {
		*failing = true
		t.failing++
	}
	if t.failures == 0 { // the outage starts
		t.since = time.Now()
		t.failures = 1
		w.logger.ErrorContext(ctx, msg, "loop", id, "error", err,
			"retry_every", w.cfg.PollInterval.String(), "note", "retries are logged at debug level")
		return
	}
	t.failures++
	w.logger.DebugContext(ctx, msg, "loop", id, "error", err)
}

// succeeded records a successful try; once no loop is failing any more, the outage's end is logged.
func (w *Worker) succeeded(ctx context.Context, failing *bool) {
	if !*failing {
		return
	}
	*failing = false
	t := &w.trouble
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failing--
	if t.failing == 0 {
		w.logger.InfoContext(ctx, "working again", "down_for", time.Since(t.since).Round(time.Millisecond).String(),
			"failed_tries", t.failures)
		t.failures = 0
	}
}

// pause sleeps one poll interval, or less if stop is cancelled.
func (w *Worker) pause(stop context.Context) {
	timer := time.NewTimer(w.cfg.PollInterval)
	defer timer.Stop()
	select {
	case <-stop.Done():
	case <-timer.C:
	}
}
