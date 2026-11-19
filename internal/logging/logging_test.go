package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// logOnce logs one line through a wrapped JSON handler and returns it parsed.
func logOnce(t *testing.T, ctx context.Context, build func(*slog.Logger) *slog.Logger) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := build(slog.New(NewHandler(slog.NewJSONHandler(&buf, nil))))
	logger.InfoContext(ctx, "hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %v\n%s", err, buf.String())
	}
	return line
}

func same(l *slog.Logger) *slog.Logger { return l }

func TestHandler_AddsCorrelationID(t *testing.T) {
	ctx := WithCorrelationID(t.Context(), "c-123")

	line := logOnce(t, ctx, same)
	if line["correlation_id"] != "c-123" {
		t.Errorf("correlation_id = %v, want c-123", line["correlation_id"])
	}
}

func TestHandler_NoCorrelationID(t *testing.T) {
	line := logOnce(t, t.Context(), same)
	if _, ok := line["correlation_id"]; ok {
		t.Errorf("correlation_id = %v, want no field", line["correlation_id"])
	}
}

// logger.With returns a new handler through WithAttrs; the ID must survive it.
func TestHandler_KeepsWorkingAfterWith(t *testing.T) {
	ctx := WithCorrelationID(t.Context(), "c-123")

	line := logOnce(t, ctx, func(l *slog.Logger) *slog.Logger { return l.With("component", "worker") })
	if line["correlation_id"] != "c-123" || line["component"] != "worker" {
		t.Errorf("line = %v, want correlation_id c-123 and component worker", line)
	}
}

func TestCorrelationID_EmptyContext(t *testing.T) {
	if id := CorrelationID(context.Background()); id != "" {
		t.Errorf("CorrelationID = %q, want empty", id)
	}
}
