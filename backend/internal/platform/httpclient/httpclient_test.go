package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func mustRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func fastPolicy() Policy {
	return Policy{
		Timeout:     200 * time.Millisecond,
		Retries:     2,
		RetryBase:   1 * time.Millisecond,
		RPS:         0, // 默认关限流，单测里按需打开
		BreakerN:    0, // 默认关熔断
		BreakerCool: 50 * time.Millisecond,
	}
}

func TestDo_SuccessAndDefaultUserAgent(t *testing.T) {
	var seenUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(fastPolicy())
	resp, err := c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if seenUA != UserAgent {
		t.Errorf("UA = %q, want %q", seenUA, UserAgent)
	}
}

func TestDo_PreservesCallerUserAgent(t *testing.T) {
	var seenUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(fastPolicy())
	req := mustRequest(t, "GET", srv.URL)
	req.Header.Set("User-Agent", "custom/1.0")
	resp, err := c.Do(context.Background(), "test", req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenUA != "custom/1.0" {
		t.Errorf("UA = %q, want preserved custom/1.0", seenUA)
	}
}

func TestDo_RetriesOn5xxAndEventuallySucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(fastPolicy())
	resp, err := c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3 (2 failures + 1 success)", got)
	}
}

func TestDo_ReturnsLast5xxAfterExhaustingRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(fastPolicy())
	_, err := c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	// fastPolicy 是 Retries=2 → 总尝试 3 次
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestDo_DoesNotRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(fastPolicy())
	resp, err := c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestDo_CircuitOpensAfterConsecutiveFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := fastPolicy()
	p.Retries = 0
	p.BreakerN = 2
	p.BreakerCool = 1 * time.Second
	c := New(p)

	// 前两次实打实失败
	for i := 0; i < 2; i++ {
		_, err := c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
		if err == nil {
			t.Fatalf("expected 5xx error on call %d", i+1)
		}
	}
	// 第三次应直接被熔断短路
	_, err := c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestDo_RateLimitSerializesCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := fastPolicy()
	p.RPS = 10 // 100ms 一个 token
	p.Burst = 1
	c := New(p)

	// 第一次：burst=1，瞬间过
	start := time.Now()
	resp, err := c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 第二次：必须等约 100ms
	resp, err = c.Do(context.Background(), "test", mustRequest(t, "GET", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("two RPS=10 calls completed in %v, expected ≥ ~100ms", elapsed)
	}
}

func TestDo_RespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 永远不返回，让请求卡在网络层
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := fastPolicy()
	p.Timeout = 50 * time.Millisecond
	p.Retries = 1
	c := New(p)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Do(ctx, "test", mustRequest(t, "GET", srv.URL))
	if err == nil {
		t.Fatal("expected error from ctx cancel")
	}
}

func TestDo_PerSourceIsolation(t *testing.T) {
	// 一个 source 熔断不应影响另一个 source
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()

	p := fastPolicy()
	p.Retries = 0
	p.BreakerN = 2
	c := New(p)

	for i := 0; i < 3; i++ {
		_, _ = c.Do(context.Background(), "bad", mustRequest(t, "GET", failing.URL))
	}
	// "bad" 已熔断；"good" 不受影响
	resp, err := c.Do(context.Background(), "good", mustRequest(t, "GET", ok.URL))
	if err != nil {
		t.Fatalf("good source should be unaffected, got %v", err)
	}
	resp.Body.Close()
}

func TestDo_EmptySourceRejected(t *testing.T) {
	c := New(fastPolicy())
	_, err := c.Do(context.Background(), "", mustRequest(t, "GET", "http://localhost"))
	if err == nil {
		t.Fatal("expected error for empty source")
	}
}

func TestDo_WithSourcePolicyOverrideTakesEffect(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(fastPolicy()) // defaults: Retries=2 → 3 calls
	c.WithSourcePolicy("strict", Policy{
		Timeout:   200 * time.Millisecond,
		Retries:   0,
		RetryBase: 1 * time.Millisecond,
	})

	_, _ = c.Do(context.Background(), "strict", mustRequest(t, "GET", srv.URL))
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (override Retries=0)", got)
	}
}

// drainBody 保证 httptest 不漏 body（即便测试里偶尔忘）
var _ = io.Discard
