package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/namelyzz/FundPilot/internal/platform/config"
)

func TestNew_JSONFormatWritesStructured(t *testing.T) {
	var buf bytes.Buffer
	lg := New(config.Logger{Level: "info", Format: "json"}, &buf)
	lg.Info("hello", "k", "v")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("expected json log, got %q: %v", buf.String(), err)
	}
	if rec["msg"] != "hello" {
		t.Errorf("msg = %v", rec["msg"])
	}
	if rec["module"] != "fundpilot" {
		t.Errorf("module attr missing: %v", rec["module"])
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v", rec["level"])
	}
}

func TestNew_ConsoleFormatPlainText(t *testing.T) {
	var buf bytes.Buffer
	lg := New(config.Logger{Level: "debug", Format: "console"}, &buf)
	lg.Info("hi")
	if strings.HasPrefix(buf.String(), "{") {
		t.Errorf("expected text format, got json-like: %q", buf.String())
	}
}

func TestNew_LevelFilter(t *testing.T) {
	var buf bytes.Buffer
	lg := New(config.Logger{Level: "warn", Format: "json"}, &buf)
	lg.Info("should be filtered")
	if buf.Len() != 0 {
		t.Errorf("info should be filtered at warn level, got %q", buf.String())
	}
	lg.Warn("should pass")
	if buf.Len() == 0 {
		t.Errorf("warn should pass")
	}
}

func TestContext_TraceIDAndLoggerPropagation(t *testing.T) {
	var buf bytes.Buffer
	root := New(config.Logger{Level: "info", Format: "json"}, &buf)

	ctx := WithTraceID(context.Background(), root, "trc-abc")

	if got := TraceIDFromContext(ctx); got != "trc-abc" {
		t.Errorf("TraceIDFromContext = %q", got)
	}

	FromContext(ctx).Info("hello")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log not json: %v / %q", err, buf.String())
	}
	if rec["trace_id"] != "trc-abc" {
		t.Errorf("trace_id missing in log: %v", rec)
	}
}

func TestFromContext_DefaultsWhenUnset(t *testing.T) {
	if FromContext(context.Background()) == nil {
		t.Fatal("FromContext should never return nil")
	}
	if TraceIDFromContext(context.Background()) != "" {
		t.Error("TraceIDFromContext should be empty for plain ctx")
	}
}
