// Package ratelimit limits how many notifications are sent per second on each channel.
// The count lives in Redis, so the limit holds for all worker processes together.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// Limiter allows at most perSecond sends per channel, shared through Redis (D2, D15).
type Limiter struct {
	rl    *redis_rate.Limiter
	limit redis_rate.Limit
}

// New returns a Limiter allowing perSecond sends per channel, with up to burst at once.
// The values are expected to be valid (config.Load checks them).
func New(client *redis.Client, perSecond, burst int) *Limiter {
	return &Limiter{
		rl:    redis_rate.NewLimiter(client),
		limit: redis_rate.Limit{Rate: perSecond, Burst: burst, Period: time.Second},
	}
}

// Wait blocks until one send on ch is allowed or ctx ends.
// Any error means no permission was given: if Redis is down, nothing is sent (fail closed).
func (l *Limiter) Wait(ctx context.Context, ch domain.Channel) error {
	for {
		// One atomic Lua script in Redis: check and take a slot together.
		res, err := l.rl.Allow(ctx, key(ch), l.limit)
		if err != nil {
			return fmt.Errorf("rate limit %s: %w", ch, err)
		}
		if res.Allowed > 0 {
			return nil
		}

		// Denied: Redis says when the next slot opens, so sleep until then and ask again.
		timer := time.NewTimer(res.RetryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for %s rate limit: %w", ch, ctx.Err())
		case <-timer.C:
		}
	}
}

// key is the Redis key of a channel's limit; the library adds "rate:" in front.
func key(ch domain.Channel) string {
	return "channel:" + string(ch)
}
