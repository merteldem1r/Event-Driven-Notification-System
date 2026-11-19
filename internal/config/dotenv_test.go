package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates a file with the given content in a temporary directory
// that Go deletes automatically after the test.
func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env.test")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDotEnv_SetsVariablesFromFile(t *testing.T) {
	const key = "DOTENV_TEST_NEW_VAR"
	// Make sure the variable starts unset, and is unset again after the test.
	t.Setenv(key, "")
	os.Unsetenv(key)

	if err := LoadDotEnv(writeFile(t, key+"=from-file\n")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv(key); got != "from-file" {
		t.Errorf("%s = %q, want %q", key, got, "from-file")
	}
}

func TestLoadDotEnv_DoesNotOverrideExistingVariables(t *testing.T) {
	const key = "DOTENV_TEST_EXISTING_VAR"
	t.Setenv(key, "from-environment") // restored automatically after the test

	if err := LoadDotEnv(writeFile(t, key+"=from-file\n")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv(key); got != "from-environment" {
		t.Errorf("%s = %q, want the existing value %q", key, got, "from-environment")
	}
}

func TestLoadDotEnv_MissingFileIsNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := LoadDotEnv(missing); err != nil {
		t.Errorf("missing file should be ignored, got error: %v", err)
	}
}

func TestLoadDotEnv_InvalidFileIsAnError(t *testing.T) {
	// godotenv is lenient about most typos; an unclosed quote is one thing it rejects.
	if err := LoadDotEnv(writeFile(t, "KEY=\"unclosed quote\n")); err == nil {
		t.Error("expected an error for an unparsable file, got nil")
	}
}
