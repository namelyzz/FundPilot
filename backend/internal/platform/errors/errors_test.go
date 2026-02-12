package errors

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/namelyzz/FundPilot/internal/platform/config"
	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body not json: %v / %q", err, rec.Body.String())
	}
	return env
}

func newReqWithTrace(traceID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	lg := logger.New(config.Logger{Level: "error", Format: "json"}, &bytes.Buffer{})
	ctx := logger.WithTraceID(r.Context(), lg, traceID)
	return r.WithContext(ctx)
}

func TestWriteOK_ShapeAndTrace(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteOK(rec, newReqWithTrace("trc-1"), map[string]string{"hello": "world"})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	env := decode(t, rec)
	if env.Code != CodeOK {
		t.Errorf("code = %q", env.Code)
	}
	if env.Meta.TraceID != "trc-1" {
		t.Errorf("trace_id missing: %+v", env)
	}
	if env.Data.(map[string]any)["hello"] != "world" {
		t.Errorf("data lost: %+v", env.Data)
	}
}

func TestWriteError_TypedErrorPreservesCodeStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, newReqWithTrace("trc-2"), ErrNotFound("no such fund"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
	env := decode(t, rec)
	if env.Code != CodeNotFound || env.Message != "no such fund" {
		t.Errorf("envelope wrong: %+v", env)
	}
	if env.Data != nil {
		t.Errorf("error response should have no data, got %v", env.Data)
	}
}

func TestWriteError_GenericErrorBecomesInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, newReqWithTrace("trc-3"), stderrors.New("boom"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d", rec.Code)
	}
	env := decode(t, rec)
	if env.Code != CodeInternalError {
		t.Errorf("code = %q", env.Code)
	}
}

func TestError_UnwrapAndMessage(t *testing.T) {
	root := stderrors.New("db down")
	e := Wrap("UPSTREAM_UNAVAILABLE", http.StatusBadGateway, "fetch failed", root)
	if !stderrors.Is(e, root) {
		t.Error("errors.Is should find wrapped cause")
	}
	if got := e.Error(); got == "" {
		t.Error("Error() empty")
	}
}

func TestWriteJSON_OmitsDataWhenNil(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.Background())
	WriteJSON(rec, r, http.StatusOK, CodeOK, "", nil)
	if bytes.Contains(rec.Body.Bytes(), []byte(`"data":`)) {
		t.Errorf("data should be omitted when nil, body=%q", rec.Body.String())
	}
}
