package config

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/joho/godotenv"
)

// LoadDotEnv reads KEY=value lines from the file at path into the process
// environment. It exists for local development only: a developer keeps their
// personal settings in a gitignored file instead of exporting them by hand.
//
//   - A missing file is not an error. Docker and production have no such file
//     (it's excluded from the image), so this is a no-op there.
//   - Variables that are already set are NOT overwritten, so a value from the
//     real environment (terminal, Docker, orchestrator) always wins over the file.
//   - A file that exists but can't be read or parsed (e.g. an unclosed quote) IS
//     an error. godotenv is lenient about most other typos, so wrong values are
//     caught later by Load's validation instead.
func LoadDotEnv(path string) error {
	err := godotenv.Load(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("load %s: %w", path, err)
}
