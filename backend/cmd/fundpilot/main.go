// Command fundpilot 是 FundPilot 后端服务入口。
//
// V0.1-B4 阶段：
//   - 加载配置（config.yaml + FUNDPILOT_* 环境变量）
//   - 初始化结构化 logger
//   - 打开 pgxpool 连接池
//   - 启动 calendar.Service（从 trade_calendar 全量加载到内存）
//   - 启动 scheduler，并注册 V0.1 计划任务（calendar.refresh / market.refresh_realtime 占位）
//   - 起 chi HTTP server，暴露 /health（含 db / scheduler / calendar 状态）
//   - 优雅关闭：收到 SIGINT/SIGTERM → server.Shutdown → scheduler.Stop → pool.Close
//
// 后续批次：REQ-04 真正实现 market.refresh_realtime；REQ-05 接 valuation.recalculate。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/namelyzz/FundPilot/internal/platform/calendar"
	"github.com/namelyzz/FundPilot/internal/platform/config"
	"github.com/namelyzz/FundPilot/internal/platform/db"
	"github.com/namelyzz/FundPilot/internal/platform/httpserver"
	"github.com/namelyzz/FundPilot/internal/platform/logger"
	"github.com/namelyzz/FundPilot/internal/platform/scheduler"
)

const version = "0.1.0-b4"

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml (use empty string to skip)")
	flag.Parse()

	if code := run(*configPath); code != 0 {
		os.Exit(code)
	}
}

func run(configPath string) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fundpilot: load config: %v\n", err)
		return 1
	}

	lg := logger.New(cfg.Logger, nil)
	slog.SetDefault(lg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// DB pool
	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pool, err := db.Open(openCtx, cfg.Database)
	cancel()
	if err != nil {
		lg.Error("db open failed", "err", err.Error())
		return 1
	}
	defer pool.Close()
	lg.Info("db pool ready", "max_open", cfg.Database.MaxOpen, "max_idle", cfg.Database.MaxIdle)

	// Calendar：启动时尝试一次刷新；失败不致命，但日志会标出来
	cal := calendar.New(pool)
	refreshCtx, refreshCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := cal.Refresh(refreshCtx); err != nil {
		lg.Warn("calendar initial refresh failed (运行 cmd/calendar-seed 再 restart)", "err", err.Error())
	} else {
		start, end, count := cal.Coverage()
		lg.Info("calendar loaded", "count", count, "start", start.Format("2006-01-02"), "end", end.Format("2006-01-02"))
	}
	refreshCancel()

	// Scheduler
	sch := scheduler.New(lg)
	if err := registerJobs(sch, cal); err != nil {
		lg.Error("scheduler register failed", "err", err.Error())
		return 1
	}
	sch.Start(ctx)

	// HTTP server
	srv := httpserver.New(httpserver.Options{
		Addr:         net.JoinHostPort("", strconv.Itoa(cfg.Server.Port)),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		Logger:       lg,
		HealthCheck:  makeHealthChecker(pool, sch, cal),
	})

	lg.Info("fundpilot starting",
		"version", version,
		"addr", srv.Addr,
		"log_level", cfg.Logger.Level,
		"log_format", cfg.Logger.Format,
	)

	serveErr := httpserver.Serve(ctx, srv, 10*time.Second)

	// 顺序：先 stop scheduler（避免任务在 pool 关闭后继续跑），再让 defer pool.Close()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := sch.Stop(stopCtx); err != nil {
		lg.Warn("scheduler stop error", "err", err.Error())
	}
	stopCancel()

	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		lg.Error("http server exited", "err", serveErr.Error())
		return 1
	}

	lg.Info("fundpilot stopped", "version", version)
	return 0
}

// registerJobs 把 V0.1 范围内的 cron 任务挂上 scheduler。
//
// 真正实现的 handler 在各自 REQ 完成时替换；这里先注册"占位 + 日志"版本，便于在
// B4 阶段就观察调度行为是否符合预期（特别是 IfTradingTime 中间件）。
func registerJobs(sch *scheduler.Scheduler, cal *calendar.Service) error {
	jobs := []struct {
		name string
		cron string
		fn   scheduler.Handler
		wrap bool // 是否包 IfTradingTime
	}{
		{
			name: "calendar.refresh",
			cron: "0 2 1 * *", // 每月 1 日 02:00
			fn: func(ctx context.Context) error {
				logger.FromContext(ctx).Warn("calendar.refresh: 未接入 Python 长驻 sidecar；请手工运行 cmd/calendar-seed 导入最新数据")
				return nil
			},
		},
		{
			name: "market.refresh_realtime",
			cron: "*/5 * * * *", // 每 5 分钟
			fn: func(ctx context.Context) error {
				logger.FromContext(ctx).Info("market.refresh_realtime: placeholder (REQ-04)")
				return nil
			},
			wrap: true,
		},
	}

	for _, j := range jobs {
		h := j.fn
		if j.wrap {
			h = scheduler.IfTradingTime(cal, h)
		}
		if err := sch.Register(j.name, j.cron, h); err != nil {
			return fmt.Errorf("register %s: %w", j.name, err)
		}
	}
	return nil
}

func makeHealthChecker(pool *pgxpool.Pool, sch *scheduler.Scheduler, cal *calendar.Service) httpserver.HealthChecker {
	return func(ctx context.Context) httpserver.HealthReport {
		report := httpserver.HealthReport{
			Status:    "ok",
			DB:        "ok",
			Scheduler: "running",
			Version:   version,
		}

		pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		if err := db.Ping(pingCtx, pool); err != nil {
			report.DB = "down"
			report.Status = "degraded"
		}

		if last := cal.LastRefresh(); !last.IsZero() {
			report.CalendarLastRefresh = last.UTC().Format(time.RFC3339)
		} else {
			report.CalendarLastRefresh = "never"
			if report.Status == "ok" {
				report.Status = "degraded"
			}
		}
		if start, end, count := cal.Coverage(); count > 0 {
			report.CalendarCoverage = fmt.Sprintf("%s ~ %s (%d days)",
				start.Format("2006-01-02"), end.Format("2006-01-02"), count)
		}

		report.Jobs = sch.Status()
		return report
	}
}
