// Package config loads application settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds the service settings.
type Config struct {
	HTTPPort int
	LogLevel slog.Level
}

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

	return Config{HTTPPort: port, LogLevel: level}, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
