package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/namelyzz/FundPilot/internal/platform/config"
	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

func newServer(t *testing.T, check HealthChecker) (*http.Server, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	lg := logger.New(config.Logger{Level: "debug", Format: "json"}, buf)
	srv := New(Options{
		Addr:        ":0",
		Logger:      lg,
		HealthCheck: check,
	})
	return srv, buf
}

func TestHealth_DefaultReport(t *testing.T) {
	srv, _ := newServer(t, func(ctx context.Context) HealthReport {
		return HealthReport{Status: "ok", DB: "ok", Scheduler: "running", Version: "0.1.0-b2"}
	})

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Code string       `json:"code"`
		Data HealthReport `json:"data"`
		Meta struct {
			TraceID string `json:"trace_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v / %s", err, rec.Body.String())
	}
	if env.Code != "OK" || env.Data.DB != "ok" {
		t.Errorf("unexpected body: %+v", env)
	}
	if env.Meta.TraceID == "" {
		t.Errorf("trace_id should be auto-generated")
	}
}

func TestHealth_HonorsIncomingRequestID(t *testing.T) {
	srv, _ := newServer(t, nil)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.Header.Set("X-Request-Id", "supplied-id")
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, r)

	if got := rec.Header().Get("X-Request-Id"); got != "supplied-id" {
		t.Errorf("X-Request-Id echoed=%q, want supplied-id", got)
	}
	var env struct {
		Meta struct {
			TraceID string `json:"trace_id"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Meta.TraceID != "supplied-id" {
		t.Errorf("meta.trace_id=%q, want supplied-id", env.Meta.TraceID)
	}
}

func TestRecover_PanicBecomesInternalError(t *testing.T) {
	buf := &bytes.Buffer{}
	lg := logger.New(config.Logger{Level: "error", Format: "json"}, buf)
	srv := New(Options{Logger: lg})
	// 注入一个会 panic 的路由
	srv.Handler.(*gin.Engine).GET("/boom", func(c *gin.Context) {
		panic("nope")
	})

	r := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("INTERNAL_ERROR")) {
		t.Errorf("body should contain INTERNAL_ERROR, got %s", rec.Body.String())
	}
}
