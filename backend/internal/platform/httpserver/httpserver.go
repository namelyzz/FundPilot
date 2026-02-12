// Package httpserver 装配 chi 路由、跨切中间件与 HTTP 服务器生命周期。
//
// 中间件链（外到内）：
//  1. RequestID    — 生成或透传 X-Request-Id
//  2. TraceLogger  — 把 trace_id 钉到 ctx 上，派生带字段的 logger 写回 ctx
//  3. AccessLog    — 请求级摘要日志（方法/路径/状态/耗时）
//  4. Recover      — panic → 500 + INTERNAL_ERROR
//
// V0.1 阶段只挂 /health；业务路由由 REQ-02 ~ REQ-06 各自实现后在 main.go 注入。
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	apperrors "github.com/namelyzz/FundPilot/internal/platform/errors"
	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

const (
	headerRequestID = "X-Request-Id"
)

// HealthChecker 由调用方注入，避免 httpserver 依赖具体的 db / scheduler 实现。
type HealthChecker func(ctx context.Context) HealthReport

// HealthReport 对齐 FR-PL-10 的响应字段。
type HealthReport struct {
	Status              string `json:"status"`
	DB                  string `json:"db"`
	Scheduler           string `json:"scheduler"`
	CalendarLastRefresh string `json:"calendar_last_refresh,omitempty"`
	Version             string `json:"version"`
}

// Options 描述构造一个 server 所需的依赖。
type Options struct {
	Addr         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	Logger       *slog.Logger
	HealthCheck  HealthChecker
}

// New 构造可启动的 http.Server，路由与中间件已挂好。
func New(opts Options) *http.Server {
	r := chi.NewRouter()

	r.Use(requestIDMiddleware)
	r.Use(traceLoggerMiddleware(opts.Logger))
	r.Use(accessLogMiddleware)
	r.Use(recoverMiddleware)

	r.Get("/health", healthHandler(opts.HealthCheck))

	return &http.Server{
		Addr:         opts.Addr,
		Handler:      r,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
	}
}

// ---- middleware ----

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(headerRequestID, id)
		// 把 id 暂存到 header 上下游可读；下一层中间件会消费它
		r.Header.Set(headerRequestID, id)
		next.ServeHTTP(w, r)
	})
}

func traceLoggerMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(headerRequestID)
			ctx := logger.WithTraceID(r.Context(), base, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// statusWriter 捕获最终写出的 HTTP 状态供 accessLog 使用。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.FromContext(r.Context()).Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rv := recover(); rv != nil {
				logger.FromContext(r.Context()).Error("panic in handler",
					"value", fmt.Sprint(rv),
					"stack", string(debug.Stack()),
				)
				cause := fmt.Errorf("panic: %v", rv)
				apperrors.WriteError(w, r, apperrors.ErrInternal(cause))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---- handlers ----

func healthHandler(check HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var report HealthReport
		if check != nil {
			report = check(r.Context())
		} else {
			report = HealthReport{Status: "ok", DB: "skipped", Scheduler: "skipped", Version: "unknown"}
		}
		apperrors.WriteOK(w, r, report)
	}
}

// Serve 启动 server 并在 ctx 取消时执行优雅关闭。返回 server 退出原因。
func Serve(ctx context.Context, srv *http.Server, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
