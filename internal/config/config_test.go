package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
