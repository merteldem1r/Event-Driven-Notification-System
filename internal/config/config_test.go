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
			c.WorkerConcurrency = 30
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
				WorkerLeaseDuration: 2 * time.Minute,
			},
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
