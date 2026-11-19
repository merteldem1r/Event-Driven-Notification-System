package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	const dbURL = "postgres://localhost:5432/db"

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": dbURL},
			want: Config{HTTPPort: 8080, LogLevel: slog.LevelInfo, DatabaseURL: dbURL},
		},
		{
			name: "custom values",
			env:  map[string]string{"DATABASE_URL": dbURL, "HTTP_PORT": "9000", "LOG_LEVEL": "debug"},
			want: Config{HTTPPort: 9000, LogLevel: slog.LevelDebug, DatabaseURL: dbURL},
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
			for _, key := range []string{"HTTP_PORT", "LOG_LEVEL", "DATABASE_URL"} {
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
