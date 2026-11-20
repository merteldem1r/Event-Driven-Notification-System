package database

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestRedisLogger_WritesJSON(t *testing.T) {
	var buf bytes.Buffer
	l := RedisLogger{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	l.Printf(t.Context(), "redis: connection pool: failed to dial after %d attempts", 5)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %q (%v)", buf.String(), err)
	}
	if line["level"] != "WARN" || line["component"] != "redis" ||
		line["msg"] != "redis: connection pool: failed to dial after 5 attempts" {
		t.Errorf("log line = %v, want a WARN from component redis with the formatted message", line)
	}
}
