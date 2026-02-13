// Package httpserver 装配 gin 路由、跨切中间件与 HTTP 服务器生命周期。
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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	apperrors "github.com/namelyzz/FundPilot/internal/platform/errors"
	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

const (
	headerRequestID = "X-Request-Id"
)

// HealthChecker 由调用方注入，避免 httpserver 依赖具体的 db / scheduler 实现。
type HealthChecker func(ctx context.Context) HealthReport

// HealthReport 对齐 FR-PL-10 的响应字段。Jobs 用 any 承载领域结构（如
// scheduler.JobStatus 列表），避免本包反向依赖业务包。
type HealthReport struct {
	Status              string `json:"status"`
	DB                  string `json:"db"`
	Scheduler           string `json:"scheduler"`
	CalendarLastRefresh string `json:"calendar_last_refresh,omitempty"`
	CalendarCoverage    string `json:"calendar_coverage,omitempty"`
	Version             string `json:"version"`
	Jobs                any    `json:"jobs,omitempty"`
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
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	r.Use(
		requestIDMiddleware(),
		traceLoggerMiddleware(opts.Logger),
		accessLogMiddleware(),
		recoverMiddleware(),
	)

	r.GET("/health", healthHandler(opts.HealthCheck))

	return &http.Server{
		Addr:         opts.Addr,
		Handler:      r,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
	}
}

// ---- middleware ----

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Header(headerRequestID, id)
		// 把 id 暂存到 header 供下游中间件读取
		c.Request.Header.Set(headerRequestID, id)
		c.Next()
	}
}

func traceLoggerMiddleware(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		ctx := logger.WithTraceID(c.Request.Context(), base, id)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.FromContext(c.Request.Context()).Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

func recoverMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rv := recover(); rv != nil {
				logger.FromContext(c.Request.Context()).Error("panic in handler",
					"value", fmt.Sprint(rv),
					"stack", string(debug.Stack()),
				)
				cause := fmt.Errorf("panic: %v", rv)
				apperrors.WriteError(c.Writer, c.Request, apperrors.ErrInternal(cause))
				c.Abort()
			}
		}()
		c.Next()
	}
}

// ---- handlers ----

func healthHandler(check HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		var report HealthReport
		if check != nil {
			report = check(c.Request.Context())
		} else {
			report = HealthReport{Status: "ok", DB: "skipped", Scheduler: "skipped", Version: "unknown"}
		}
		apperrors.WriteOK(c.Writer, c.Request, report)
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
