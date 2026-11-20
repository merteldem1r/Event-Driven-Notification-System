package database

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// ConnectRedis creates a Redis client and checks that Redis answers.
func ConnectRedis(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	opts.ContextTimeoutEnabled = true // a context's deadline also ends a call stuck reading or writing
	client := redis.NewClient(opts)

	// NewClient doesn't connect yet; Ping makes sure Redis is reachable.
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close() // the ping error is the one worth returning
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

// RedisLogger passes the Redis library's own messages to slog, so they are JSON like every
// other log line. Install it with redis.SetLogger.
type RedisLogger struct {
	Logger *slog.Logger
}

// Printf logs one library message at debug level: the failures it reports (e.g. a refused
// connection) also come back to our code as errors, which the worker logs once per outage.
func (l RedisLogger) Printf(ctx context.Context, format string, v ...any) {
	l.Logger.DebugContext(ctx, fmt.Sprintf(format, v...), "component", "redis")
}
