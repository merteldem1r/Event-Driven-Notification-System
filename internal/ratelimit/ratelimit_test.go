package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/database"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// redisURL points to a throwaway Redis container shared by all tests in this package.
var redisURL string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests starts Redis, runs the tests, then removes the container.
func runTests(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine") // same image as docker-compose.yml
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "terminate container:", err)
		}
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start redis container (is Docker running?):", err)
		return 1
	}
	redisURL, err = ctr.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connection string:", err)
		return 1
	}
	return m.Run()
}

// connect opens a new Redis client, like each worker process does.
func connect(t *testing.T) *redis.Client {
	t.Helper()
	client, err := database.ConnectRedis(t.Context(), redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() }) // nothing to do if closing fails in a test
	return client
}

// cleanRedis removes the counts left by earlier tests.
func cleanRedis(t *testing.T) {
	t.Helper()
	if err := connect(t).FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
}

// waitWithin calls Wait with a deadline, so a denied send returns instead of blocking.
func waitWithin(l *Limiter, ch domain.Channel, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return l.Wait(ctx, ch)
}

func TestWait_AllowsBurstThenMakesYouWait(t *testing.T) {
	cleanRedis(t)
	l := New(connect(t), 2, 3) // 2 per second, 3 at once: a new slot every 500 ms

	for i := range 3 {
		if err := waitWithin(l, domain.ChannelSMS, 100*time.Millisecond); err != nil {
			t.Fatalf("send %d of the burst: %v", i+1, err)
		}
	}
	// The burst is used up and the next slot is ~500 ms away.
	err := waitWithin(l, domain.ChannelSMS, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("4th send: error = %v, want deadline exceeded", err)
	}
	// Once the slot opens, Wait lets the send through.
	if err := waitWithin(l, domain.ChannelSMS, time.Second); err != nil {
		t.Fatalf("send after waiting: %v", err)
	}
}

func TestWait_ChannelsHaveTheirOwnLimit(t *testing.T) {
	cleanRedis(t)
	l := New(connect(t), 1, 1)

	if err := waitWithin(l, domain.ChannelSMS, 100*time.Millisecond); err != nil {
		t.Fatalf("first sms: %v", err)
	}
	// SMS is used up, but email and push still have their own slot.
	for _, ch := range []domain.Channel{domain.ChannelEmail, domain.ChannelPush} {
		if err := waitWithin(l, ch, 100*time.Millisecond); err != nil {
			t.Errorf("first %s after sms used its limit: %v", ch, err)
		}
	}
	if err := waitWithin(l, domain.ChannelSMS, 100*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second sms: error = %v, want deadline exceeded", err)
	}
}

// Two limiters with their own Redis clients stand in for two worker processes (IMP-1).
// Together they must stay at 100/s, not 100/s each.
func TestWait_LimitIsSharedAcrossProcesses(t *testing.T) {
	cleanRedis(t)
	const perSecond, burst, loopsPerProcess = 100, 10, 20
	processes := []*Limiter{New(connect(t), perSecond, burst), New(connect(t), perSecond, burst)}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()

	var sent [2]atomic.Int64 // per process
	var wg sync.WaitGroup
	for p, l := range processes {
		for range loopsPerProcess {
			wg.Go(func() {
				for l.Wait(ctx, domain.ChannelSMS) == nil {
					sent[p].Add(1)
				}
			})
		}
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()

	total := sent[0].Load() + sent[1].Load()
	// At most the burst plus one slot per 10 ms; +2 for the edges of the time window.
	maxAllowed := int64(burst + perSecond*elapsed + 2)
	if total > maxAllowed {
		t.Errorf("sent %d in %.2fs, want at most %d", total, elapsed, maxAllowed)
	}
	// The limit should also be reached, not only respected.
	if minWanted := int64(0.8 * perSecond * elapsed); total < minWanted {
		t.Errorf("sent %d in %.2fs, want at least %d", total, elapsed, minWanted)
	}
	if sent[0].Load() == 0 || sent[1].Load() == 0 {
		t.Errorf("each process should send some: got %d and %d", sent[0].Load(), sent[1].Load())
	}
	t.Logf("two processes, %d loops each: %d + %d = %d sends in %.2fs",
		loopsPerProcess, sent[0].Load(), sent[1].Load(), total, elapsed)
}

// A Redis that accepts the connection but never answers must not hold the caller for long.
func TestWait_SilentRedisFailsWithinTheCallTimeout(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() }) // nothing to do if closing fails in a test
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			t.Cleanup(func() { _ = conn.Close() }) // read nothing, answer nothing
		}
	}()
	client := redis.NewClient(&redis.Options{Addr: ln.Addr().String(), MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = client.Close() }) // nothing to do if closing fails in a test

	start := time.Now()
	err = New(client, 100, 10).Wait(t.Context(), domain.ChannelSMS)
	if err == nil {
		t.Fatal("Wait returned nil from a Redis that never answers, want an error (fail closed)")
	}
	if took := time.Since(start); took > callTimeout+time.Second {
		t.Errorf("Wait took %v, want about the call timeout (%v)", took, callTimeout)
	}
}

func TestWait_RedisDownMeansNoSend(t *testing.T) {
	// Nothing listens on port 1, so every call to Redis fails.
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() }) // nothing to do if closing fails in a test
	l := New(client, 100, 10)

	err := waitWithin(l, domain.ChannelSMS, time.Second)
	if err == nil {
		t.Fatal("Wait returned nil with Redis down, want an error (fail closed)")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the Redis error right away, not a timeout", err)
	}
}
