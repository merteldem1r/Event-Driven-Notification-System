package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// These tests run in a synctest bubble: time is virtual, so a 25 s shutdown timeout takes no
// real time, and synctest.Wait returns once every goroutine is blocked, making timing exact.

var testConfig = Config{Concurrency: 1, PollInterval: 500 * time.Millisecond, ShutdownTimeout: 25 * time.Second}

// fakeProcessor hands out `queue` notifications. If block is set, each call waits for it to be
// closed (or for its context to end), like a slow provider call. Wait fails with waitErr, or
// with waitBlock blocks until its context ends, like a rate limit with no free slot.
type fakeProcessor struct {
	mu        sync.Mutex
	queue     int
	err       error
	block     chan struct{}
	waitErr   error
	waitBlock bool
	waits     int
	calls     int
	processed int
	cancelled bool // a call saw its context end
}

func (f *fakeProcessor) Wait(ctx context.Context) error {
	f.mu.Lock()
	f.waits++
	block, err := f.waitBlock, f.waitErr
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

// setWaitErr changes what Wait returns, e.g. nil once "Redis is back".
func (f *fakeProcessor) setWaitErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitErr = err
}

// syncBuffer collects log output written by many loops at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines returns the JSON log lines whose msg is msg.
func (b *syncBuffer) lines(t *testing.T, msg string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var found []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(b.buf.Bytes()), []byte("\n")) {
		var line map[string]any
		if err := json.Unmarshal(l, &line); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		if line["msg"] == msg {
			found = append(found, line)
		}
	}
	return found
}

func (f *fakeProcessor) ProcessNext(ctx context.Context) (bool, error) {
	f.mu.Lock()
	f.calls++
	if f.err != nil {
		f.mu.Unlock()
		return false, f.err
	}
	if f.queue == 0 {
		f.mu.Unlock()
		return false, nil
	}
	f.queue--
	f.mu.Unlock()

	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			f.mu.Lock()
			f.cancelled = true
			f.mu.Unlock()
			return true, ctx.Err()
		}
	}
	f.mu.Lock()
	f.processed++
	f.mu.Unlock()
	return true, nil
}

// waitCount returns how often Wait was called.
func (f *fakeProcessor) waitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waits
}

// snapshot returns the counters under the lock.
func (f *fakeProcessor) snapshot() (calls, processed int, cancelled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.processed, f.cancelled
}

// start runs a worker with one processor in the background; the channel receives Run's result.
func start(proc Processor, cfg Config) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New([]Processor{proc}, cfg, slog.New(slog.DiscardHandler)).Run(ctx) }()
	return cancel, done
}

// returned reports whether Run has returned, and its result.
func returned(done <-chan error) (bool, error) {
	select {
	case err := <-done:
		return true, err
	default:
		return false, nil
	}
}

func TestRun_ProcessesQueueThenPolls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := &fakeProcessor{queue: 100}
		cfg := testConfig
		cfg.Concurrency = 4
		cancel, done := start(proc, cfg)

		synctest.Wait() // the queue is drained without any waiting; each loop then found it empty once
		if calls, processed, _ := proc.snapshot(); processed != 100 || calls != 104 {
			t.Fatalf("calls %d, processed %d; want 104 and 100", calls, processed)
		}

		time.Sleep(cfg.PollInterval) // idle loops check again once per interval, no faster
		synctest.Wait()
		if calls, _, _ := proc.snapshot(); calls != 108 {
			t.Errorf("calls after one poll interval = %d, want 108", calls)
		}

		cancel()
		synctest.Wait()
		if ok, err := returned(done); !ok || err != nil {
			t.Errorf("Run returned %v, %v; want it stopped without error", ok, err)
		}
	})
}

// Each processor has its own loops, so one that is stuck can't hold up another (D16):
// a backlog on one channel never stops the other channels.
func TestRun_EachProcessorHasItsOwnLoops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stuck := &fakeProcessor{queue: 100, block: make(chan struct{})} // every call hangs
		free := &fakeProcessor{queue: 50}
		cfg := testConfig
		cfg.Concurrency = 2
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- New([]Processor{stuck, free}, cfg, slog.New(slog.DiscardHandler)).Run(ctx) }()

		synctest.Wait()
		if calls, _, _ := stuck.snapshot(); calls != 2 {
			t.Errorf("stuck processor: %d calls, want 2: its own 2 loops, both hanging", calls)
		}
		if _, processed, _ := free.snapshot(); processed != 50 {
			t.Errorf("other processor processed %d, want all 50 while the first is stuck", processed)
		}

		cancel()
		close(stuck.block) // let the hanging calls finish so Run can return
		if err := <-done; err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	})
}

// A loop waiting for a rate-limit slot has taken nothing yet, so the stop signal ends the wait
// at once and no notification is claimed afterwards.
func TestRun_StopsWhileWaitingForASlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := &fakeProcessor{queue: 5, waitBlock: true}
		cancel, done := start(proc, testConfig)
		synctest.Wait() // the loop is waiting for a slot

		before := time.Now()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if waited := time.Since(before); waited != 0 {
			t.Errorf("stopping took %v, want no wait", waited)
		}
		if calls, _, _ := proc.snapshot(); calls != 0 {
			t.Errorf("ProcessNext called %d times, want 0: nothing is taken without a slot", calls)
		}
	})
}

// If permission can't be checked (e.g. Redis down), nothing is taken (fail closed), and the
// loop asks again once per poll interval instead of spinning.
func TestRun_WaitErrorTakesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := &fakeProcessor{queue: 5, waitErr: errors.New("redis down")}
		cancel, done := start(proc, testConfig)
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run = %v, want nil", err)
			}
		}()

		synctest.Wait()
		if n := proc.waitCount(); n != 1 {
			t.Fatalf("Wait called %d times, want 1 before the poll interval", n)
		}
		time.Sleep(testConfig.PollInterval)
		synctest.Wait()
		if n := proc.waitCount(); n != 2 {
			t.Errorf("Wait called %d times, want 2: one retry per interval", n)
		}
		if calls, _, _ := proc.snapshot(); calls != 0 {
			t.Errorf("ProcessNext called %d times, want 0", calls)
		}
	})
}

// An outage is logged twice, when it starts and when it ends, not on every retry of every loop.
func TestRun_LogsAnOutageOnceNotEveryRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := &fakeProcessor{waitErr: errors.New("redis down")}
		var logs syncBuffer
		cfg := testConfig
		cfg.Concurrency = 3
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- New([]Processor{proc}, cfg, slog.New(slog.NewJSONHandler(&logs, nil))).Run(ctx) }()

		time.Sleep(10 * cfg.PollInterval) // 3 loops retry about 10 times each
		synctest.Wait()
		failedTries := proc.waitCount()
		proc.setWaitErr(nil) // Redis is back
		time.Sleep(cfg.PollInterval)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}

		if got := logs.lines(t, "waiting to process failed"); len(got) != 1 {
			t.Errorf("%d error lines, want 1 for the whole outage (of %d failed tries)", len(got), failedTries)
		}
		again := logs.lines(t, "working again")
		if len(again) != 1 {
			t.Fatalf("%d \"working again\" lines, want 1", len(again))
		}
		if tries := int(again[0]["failed_tries"].(float64)); tries != failedTries {
			t.Errorf("failed_tries = %d, want %d", tries, failedTries)
		}
		// The last failed retry was at 5 s; the next one, half a second later, works.
		if again[0]["down_for"] != "5.5s" {
			t.Errorf("down_for = %v, want 5.5s", again[0]["down_for"])
		}
	})
}

func TestRun_StopsIdleLoopsAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cancel, done := start(&fakeProcessor{}, testConfig)
		synctest.Wait()

		before := time.Now()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if waited := time.Since(before); waited != 0 {
			t.Errorf("stopping took %v, want no wait for the poll interval", waited)
		}
	})
}

func TestRun_FinishesInFlightWorkOnStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := &fakeProcessor{queue: 5, block: make(chan struct{})}
		cancel, done := start(proc, testConfig)
		synctest.Wait() // the loop is in the middle of a "provider call"

		cancel() // the stop signal arrives
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if ok, _ := returned(done); ok {
			t.Fatal("Run returned while work was still in flight")
		}
		if _, _, cancelled := proc.snapshot(); cancelled {
			t.Fatal("in-flight work was cancelled by the stop signal")
		}

		close(proc.block) // the call completes
		synctest.Wait()
		if ok, err := returned(done); !ok || err != nil {
			t.Fatalf("Run returned %v, %v; want it stopped without error", ok, err)
		}
		if calls, processed, _ := proc.snapshot(); calls != 1 || processed != 1 {
			t.Errorf("calls %d, processed %d; want 1 and 1: nothing new taken after the stop", calls, processed)
		}
	})
}

func TestRun_CancelsWorkAfterShutdownTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := &fakeProcessor{queue: 1, block: make(chan struct{})} // never closed: a stuck call
		cancel, done := start(proc, testConfig)
		synctest.Wait()

		cancel()
		time.Sleep(testConfig.ShutdownTimeout - time.Millisecond)
		synctest.Wait()
		if ok, _ := returned(done); ok {
			t.Fatal("Run returned before the shutdown timeout")
		}

		time.Sleep(time.Millisecond)
		synctest.Wait()
		ok, err := returned(done)
		if !ok || !errors.Is(err, ErrShutdownTimeout) {
			t.Fatalf("Run returned %v, %v; want ErrShutdownTimeout right at the deadline", ok, err)
		}
		if _, _, cancelled := proc.snapshot(); !cancelled {
			t.Error("the stuck call's context was not cancelled")
		}
	})
}

func TestRun_WaitsAfterAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := &fakeProcessor{err: errors.New("database down")}
		cancel, done := start(proc, testConfig)
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run = %v, want nil", err)
			}
		}()

		synctest.Wait()
		if calls, _, _ := proc.snapshot(); calls != 1 {
			t.Fatalf("calls = %d, want 1 before the poll interval", calls)
		}
		time.Sleep(testConfig.PollInterval)
		synctest.Wait()
		if calls, _, _ := proc.snapshot(); calls != 2 {
			t.Errorf("calls = %d, want 2: one retry per interval, not a tight loop", calls)
		}
	})
}
