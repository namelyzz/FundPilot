// Package logger 实现 FR-PL-03：基于 log/slog 的结构化日志。
//
// 输出选择：
//   - format=json     → 生产模式，使用 slog.JSONHandler，便于采集
//   - format=console  → 开发模式，使用 slog.TextHandler（避免引入彩色库依赖）
//
// 调用约定：业务代码不要拿到 logger 后到处塞字段；走 ctx：
//
//	ctx = logger.WithTraceID(ctx, traceID)
//	logger.FromContext(ctx).Info("...")
//
// FromContext 返回的 logger 已经带有 trace_id 字段。
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/namelyzz/FundPilot/internal/platform/config"
)

// ctxKey 私有，避免外部覆盖。
type ctxKey int

const (
	traceIDKey ctxKey = iota + 1
	loggerKey
)

// New 按配置构造 root logger。w 为 nil 时输出到 stderr。
func New(cfg config.Logger, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}

	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}

	var h slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "console":
		h = slog.NewTextHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}

	return slog.New(h.WithAttrs([]slog.Attr{
		slog.String("module", "fundpilot"),
	}))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithTraceID 把 trace_id 钉到 ctx 上，并把 logger 也派生一份带 trace_id 的写回 ctx。
//
// base 一般是 New() 返回的 root logger；调用方通常一次性在请求中间件里写入，
// 后续 handler 用 FromContext(ctx) 即可拿到带字段的 logger。
func WithTraceID(ctx context.Context, base *slog.Logger, traceID string) context.Context {
	if base == nil {
		base = slog.Default()
	}
	ctx = context.WithValue(ctx, traceIDKey, traceID)
	ctx = context.WithValue(ctx, loggerKey, base.With(slog.String("trace_id", traceID)))
	return ctx
}

// TraceIDFromContext 取 trace_id；不存在返回空串。
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// FromContext 取 ctx 上的 logger；未设置则返回 slog.Default()。
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
