// Package config loads application settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds the service settings. The API and the worker each use the parts they need.
type Config struct {
	HTTPPort    int
	LogLevel    slog.Level
	DatabaseURL string

	WorkerConcurrency     int           // notifications processed in parallel per worker process
	WorkerPollInterval    time.Duration // idle wait before checking the queue again
	WorkerShutdownTimeout time.Duration // time in-flight work gets after a stop signal
	WorkerLeaseDuration   time.Duration // how long a claimed notification belongs to its worker

	ProviderURL     string        // where the worker sends notifications (D9: webhook.site); empty if unset
	ProviderTimeout time.Duration // limit for one send

	RedisURL           string // Redis for the shared rate limiter; empty if unset
	RateLimitPerSecond int    // sends per second per channel, for all workers together
	RateLimitBurst     int    // sends allowed at once after an idle moment
}

// MaxWorkerConcurrency caps WORKER_CONCURRENCY; more loops than this only add database load.
const MaxWorkerConcurrency = 1000

// MaxRateLimitPerSecond caps RATE_LIMIT_PER_SECOND, a sanity bound well above the required 100.
const MaxRateLimitPerSecond = 100000

// LoadDotEnv loads variables from a local env file if it exists.
// Variables that are already set are not overwritten.
func LoadDotEnv(path string) error {
	err := godotenv.Load(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil // no file in Docker and production
	}
	return fmt.Errorf("load %s: %w", path, err)
}

// Load reads settings from the environment, using defaults for unset values.
func Load() (Config, error) {
	port, err := strconv.Atoi(getEnv("HTTP_PORT", "8080"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_PORT must be a number: %w", err)
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(getEnv("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error: %w", err)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	concurrency, err := strconv.Atoi(getEnv("WORKER_CONCURRENCY", "30"))
	if err != nil || concurrency < 1 || concurrency > MaxWorkerConcurrency {
		return Config{}, fmt.Errorf("WORKER_CONCURRENCY must be a number from 1 to %d", MaxWorkerConcurrency)
	}
	poll, err := positiveDuration("WORKER_POLL_INTERVAL", "500ms")
	if err != nil {
		return Config{}, err
	}
	shutdown, err := positiveDuration("WORKER_SHUTDOWN_TIMEOUT", "25s")
	if err != nil {
		return Config{}, err
	}

	lease, err := positiveDuration("WORKER_LEASE_DURATION", "60s") // final number is D8
	if err != nil {
		return Config{}, err
	}

	providerURL := os.Getenv("PROVIDER_URL")
	if providerURL != "" {
		u, err := url.Parse(providerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Config{}, errors.New("PROVIDER_URL must be a full http(s) URL, e.g. https://webhook.site/<your-id>")
		}
	}
	providerTimeout, err := positiveDuration("PROVIDER_TIMEOUT", "5s")
	if err != nil {
		return Config{}, err
	}
	// A send outliving its lease could be taken over by another worker and delivered twice.
	if lease <= providerTimeout {
		return Config{}, errors.New("WORKER_LEASE_DURATION must be longer than PROVIDER_TIMEOUT")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL != "" {
		u, err := url.Parse(redisURL)
		if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Host == "" {
			return Config{}, errors.New("REDIS_URL must be a full redis:// URL, e.g. redis://localhost:6379/0")
		}
	}
	perSecond, err := strconv.Atoi(getEnv("RATE_LIMIT_PER_SECOND", "100"))
	if err != nil || perSecond < 1 || perSecond > MaxRateLimitPerSecond {
		return Config{}, fmt.Errorf("RATE_LIMIT_PER_SECOND must be a number from 1 to %d", MaxRateLimitPerSecond)
	}
	// A burst above the rate would let more than the rate through in one second.
	burst, err := strconv.Atoi(getEnv("RATE_LIMIT_BURST", "10"))
	if err != nil || burst < 1 || burst > perSecond {
		return Config{}, errors.New("RATE_LIMIT_BURST must be a number from 1 to RATE_LIMIT_PER_SECOND")
	}

	return Config{
		HTTPPort: port, LogLevel: level, DatabaseURL: dbURL,
		WorkerConcurrency: concurrency, WorkerPollInterval: poll, WorkerShutdownTimeout: shutdown,
		WorkerLeaseDuration: lease,
		ProviderURL:         providerURL, ProviderTimeout: providerTimeout,
		RedisURL: redisURL, RateLimitPerSecond: perSecond, RateLimitBurst: burst,
	}, nil
}

// positiveDuration reads a Go duration such as "500ms" or "25s".
func positiveDuration(key, fallback string) (time.Duration, error) {
	d, err := time.ParseDuration(getEnv(key, fallback))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 500ms or 25s", key)
	}
	return d, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
