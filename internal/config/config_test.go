package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	const dbURL = "postgres://localhost:5432/db"
	// withDefaults fills in the worker defaults, so cases only spell out what they change.
	withDefaults := func(c Config) Config {
		if c.WorkerConcurrency == 0 {
			c.WorkerConcurrency = 10
		}
		if c.WorkerPollInterval == 0 {
			c.WorkerPollInterval = 500 * time.Millisecond
		}
		if c.WorkerShutdownTimeout == 0 {
			c.WorkerShutdownTimeout = 25 * time.Second
		}
		if c.WorkerLeaseDuration == 0 {
			c.WorkerLeaseDuration = 60 * time.Second
		}
		if c.ProviderTimeout == 0 {
			c.ProviderTimeout = 5 * time.Second
		}
		if c.RateLimitPerSecond == 0 {
			c.RateLimitPerSecond = 85
		}
		if c.RateLimitBurst == 0 {
			c.RateLimitBurst = 5
		}
		return c
	}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": dbURL},
			want: withDefaults(Config{HTTPPort: 8080, LogLevel: slog.LevelInfo, DatabaseURL: dbURL}),
		},
		{
			name: "custom values",
			env:  map[string]string{"DATABASE_URL": dbURL, "HTTP_PORT": "9000", "LOG_LEVEL": "debug"},
			want: withDefaults(Config{HTTPPort: 9000, LogLevel: slog.LevelDebug, DatabaseURL: dbURL}),
		},
		{
			name: "custom worker values",
			env: map[string]string{
				"DATABASE_URL": dbURL, "WORKER_CONCURRENCY": "5",
				"WORKER_POLL_INTERVAL": "2s", "WORKER_SHUTDOWN_TIMEOUT": "1m", "WORKER_LEASE_DURATION": "2m",
			},
			want: Config{
				HTTPPort: 8080, LogLevel: slog.LevelInfo, DatabaseURL: dbURL,
				WorkerConcurrency: 5, WorkerPollInterval: 2 * time.Second, WorkerShutdownTimeout: time.Minute,
				WorkerLeaseDuration: 2 * time.Minute, ProviderTimeout: 5 * time.Second,
				RateLimitPerSecond: 85, RateLimitBurst: 5,
			},
		},
		{
			name: "provider settings",
			env: map[string]string{
				"DATABASE_URL": dbURL, "PROVIDER_URL": "https://webhook.site/abc", "PROVIDER_TIMEOUT": "2s",
			},
			want: withDefaults(Config{
				HTTPPort: 8080, LogLevel: slog.LevelInfo, DatabaseURL: dbURL,
				ProviderURL: "https://webhook.site/abc", ProviderTimeout: 2 * time.Second,
			}),
		},
		{
			name:    "provider url without scheme",
			env:     map[string]string{"DATABASE_URL": dbURL, "PROVIDER_URL": "webhook.site/abc"},
			wantErr: "PROVIDER_URL must be a full http(s) URL",
		},
		{
			name:    "provider url with another scheme",
			env:     map[string]string{"DATABASE_URL": dbURL, "PROVIDER_URL": "ftp://example.com/x"},
			wantErr: "PROVIDER_URL must be a full http(s) URL",
		},
		{
			name: "rate limit settings",
			env: map[string]string{
				"DATABASE_URL": dbURL, "REDIS_URL": "redis://localhost:6379/0",
				"RATE_LIMIT_PER_SECOND": "50", "RATE_LIMIT_BURST": "50",
			},
			want: withDefaults(Config{
				HTTPPort: 8080, LogLevel: slog.LevelInfo, DatabaseURL: dbURL,
				RedisURL: "redis://localhost:6379/0", RateLimitPerSecond: 50, RateLimitBurst: 50,
			}),
		},
		{
			name:    "redis url without scheme",
			env:     map[string]string{"DATABASE_URL": dbURL, "REDIS_URL": "localhost:6379"},
			wantErr: "REDIS_URL must be a full redis:// URL",
		},
		{
			name:    "redis url with another scheme",
			env:     map[string]string{"DATABASE_URL": dbURL, "REDIS_URL": "http://localhost:6379"},
			wantErr: "REDIS_URL must be a full redis:// URL",
		},
		{
			name:    "rate limit 0",
			env:     map[string]string{"DATABASE_URL": dbURL, "RATE_LIMIT_PER_SECOND": "0"},
			wantErr: "RATE_LIMIT_PER_SECOND must be a number from 1 to 100000",
		},
		{
			name:    "rate limit not a number",
			env:     map[string]string{"DATABASE_URL": dbURL, "RATE_LIMIT_PER_SECOND": "fast"},
			wantErr: "RATE_LIMIT_PER_SECOND must be",
		},
		{
			name:    "burst 0",
			env:     map[string]string{"DATABASE_URL": dbURL, "RATE_LIMIT_BURST": "0"},
			wantErr: "RATE_LIMIT_BURST must be a number from 1 to RATE_LIMIT_PER_SECOND",
		},
		{
			name:    "burst above the rate",
			env:     map[string]string{"DATABASE_URL": dbURL, "RATE_LIMIT_PER_SECOND": "100", "RATE_LIMIT_BURST": "101"},
			wantErr: "RATE_LIMIT_BURST must be",
		},
		{
			name: "lease not longer than provider timeout",
			env: map[string]string{
				"DATABASE_URL": dbURL, "PROVIDER_TIMEOUT": "60s", "WORKER_LEASE_DURATION": "60s",
			},
			wantErr: "WORKER_LEASE_DURATION must be longer than PROVIDER_TIMEOUT",
		},
		{
			name:    "concurrency not a number",
			env:     map[string]string{"DATABASE_URL": dbURL, "WORKER_CONCURRENCY": "many"},
			wantErr: "WORKER_CONCURRENCY must be a number from 1 to 1000",
		},
		{
			name:    "concurrency 0",
			env:     map[string]string{"DATABASE_URL": dbURL, "WORKER_CONCURRENCY": "0"},
			wantErr: "WORKER_CONCURRENCY must be",
		},
		{
			name:    "concurrency too high",
			env:     map[string]string{"DATABASE_URL": dbURL, "WORKER_CONCURRENCY": "1001"},
			wantErr: "WORKER_CONCURRENCY must be",
		},
		{
			name:    "poll interval without unit",
			env:     map[string]string{"DATABASE_URL": dbURL, "WORKER_POLL_INTERVAL": "500"},
			wantErr: "WORKER_POLL_INTERVAL must be a positive duration",
		},
		{
			name:    "negative shutdown timeout",
			env:     map[string]string{"DATABASE_URL": dbURL, "WORKER_SHUTDOWN_TIMEOUT": "-5s"},
			wantErr: "WORKER_SHUTDOWN_TIMEOUT must be a positive duration",
		},
		{
			name:    "missing database url",
			env:     map[string]string{},
			wantErr: "DATABASE_URL is required",
		},
		{
			name:    "invalid port",
			env:     map[string]string{"DATABASE_URL": dbURL, "HTTP_PORT": "abc"},
			wantErr: "HTTP_PORT must be a number",
		},
		{
			name:    "invalid log level",
			env:     map[string]string{"DATABASE_URL": dbURL, "LOG_LEVEL": "loud"},
			wantErr: "LOG_LEVEL must be",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Start from a clean slate: empty values count as unset.
			for _, key := range []string{
				"HTTP_PORT", "LOG_LEVEL", "DATABASE_URL",
				"WORKER_CONCURRENCY", "WORKER_POLL_INTERVAL", "WORKER_SHUTDOWN_TIMEOUT", "WORKER_LEASE_DURATION",
				"PROVIDER_URL", "PROVIDER_TIMEOUT",
				"REDIS_URL", "RATE_LIMIT_PER_SECOND", "RATE_LIMIT_BURST",
			} {
				t.Setenv(key, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := Load()

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadFakeProvider(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    FakeProvider
		wantErr string
	}{
		{
			name: "defaults, no database needed",
			env:  map[string]string{},
			want: FakeProvider{Port: 9090, LogLevel: slog.LevelInfo, Latency: 50 * time.Millisecond},
		},
		{
			name: "custom values",
			env: map[string]string{
				"FAKE_PROVIDER_PORT": "7000", "LOG_LEVEL": "debug",
				"FAKE_PROVIDER_LATENCY": "0s", "FAKE_PROVIDER_FAILURE_RATE": "0.25",
			},
			want: FakeProvider{Port: 7000, LogLevel: slog.LevelDebug, FailureRate: 0.25},
		},
		{
			name:    "port out of range",
			env:     map[string]string{"FAKE_PROVIDER_PORT": "70000"},
			wantErr: "FAKE_PROVIDER_PORT must be a port number",
		},
		{
			name:    "negative latency",
			env:     map[string]string{"FAKE_PROVIDER_LATENCY": "-1s"},
			wantErr: "FAKE_PROVIDER_LATENCY must be",
		},
		{
			name:    "latency too long",
			env:     map[string]string{"FAKE_PROVIDER_LATENCY": "2m"},
			wantErr: "FAKE_PROVIDER_LATENCY must be",
		},
		{
			name:    "failure rate above 1",
			env:     map[string]string{"FAKE_PROVIDER_FAILURE_RATE": "1.5"},
			wantErr: "FAKE_PROVIDER_FAILURE_RATE must be a number from 0 to 1",
		},
		{
			name:    "failure rate as percent",
			env:     map[string]string{"FAKE_PROVIDER_FAILURE_RATE": "10%"},
			wantErr: "FAKE_PROVIDER_FAILURE_RATE must be",
		},
		{
			name:    "invalid log level",
			env:     map[string]string{"LOG_LEVEL": "loud"},
			wantErr: "LOG_LEVEL must be",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{
				"DATABASE_URL", "LOG_LEVEL", "FAKE_PROVIDER_PORT", "FAKE_PROVIDER_LATENCY", "FAKE_PROVIDER_FAILURE_RATE",
			} {
				t.Setenv(key, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := LoadFakeProvider()

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// writeEnvFile creates a temporary env file with the given content.
func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env.test")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDotEnv_SetsVariablesFromFile(t *testing.T) {
	const key = "DOTENV_TEST_NEW_VAR"
	t.Setenv(key, "") // restores the original value after the test
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}

	if err := LoadDotEnv(writeEnvFile(t, key+"=from-file\n")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv(key); got != "from-file" {
		t.Errorf("%s = %q, want %q", key, got, "from-file")
	}
}

func TestLoadDotEnv_DoesNotOverrideExistingVariables(t *testing.T) {
	const key = "DOTENV_TEST_EXISTING_VAR"
	t.Setenv(key, "from-environment")

	if err := LoadDotEnv(writeEnvFile(t, key+"=from-file\n")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv(key); got != "from-environment" {
		t.Errorf("%s = %q, want %q", key, got, "from-environment")
	}
}

func TestLoadDotEnv_MissingFileIsNotAnError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadDotEnv_InvalidFileIsAnError(t *testing.T) {
	// godotenv rejects an unclosed quote.
	if err := LoadDotEnv(writeEnvFile(t, "KEY=\"unclosed\n")); err == nil {
		t.Error("expected an error, got nil")
	}
}
