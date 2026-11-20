package database

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ConnectRedis creates a Redis client and checks that Redis answers.
func ConnectRedis(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opts)

	// NewClient doesn't connect yet; Ping makes sure Redis is reachable.
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close() // the ping error is the one worth returning
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
