// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// Config holds the settings the service needs.
// New fields get added as later phases need them (database URL, Redis URL, ...).
type Config struct {
	HTTPPort int
	LogLevel slog.Level
}

// Load reads settings from the environment and falls back to defaults.
// It returns an error for values that are present but invalid, so a typo
// fails loudly at startup instead of being silently ignored.
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
