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
	// 禁用 Gin 的调试输出（控制台彩色日志）
	gin.SetMode(gin.ReleaseMode)
	// 为什么用 gin.New() 而不是 gin.Default()？
	// gin.Default() 自带 Logger 和 Recovery 中间件，
	// 但我们自己实现了更精细的版本（带 trace_id、用 slog 输出、返回统一错误格式），
	// 所以用 gin.New() 从零挂载。
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

// requestIDMiddleware 执行流程
// 请求进来（无 X-Request-Id）
//   → 生成 UUID: "abc-123"
//   → 响应头写入 X-Request-Id: abc-123
//   → 请求头也写入 X-Request-Id: abc-123
//   → c.Next() 进入下一个中间件

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
		// Go 的 context 是不可变的, 需要把上面的 ctx 挂回 c.Request
		// 如果不写这行，后续的日志里就没有 trace_id 了
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// accessLogMiddleware  记录每个 HTTP 请求的访问日志——方法、路径、状态码、耗时。
func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next() // 先执行后续 handler
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
				c.Abort() // 出错了，阻止后续 handler 执行，直接终止链
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
