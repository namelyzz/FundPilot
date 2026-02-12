// Command fundpilot 是 FundPilot 后端服务入口。
//
// V0.1-B2 阶段：
//   - 加载配置（config.yaml + FUNDPILOT_* 环境变量）
//   - 初始化结构化 logger
//   - 打开 pgxpool 连接池
//   - 起 chi HTTP server，仅暴露 /health
//   - 优雅关闭：收到 SIGINT/SIGTERM → server.Shutdown → pool.Close
//
// 后续批次：B3 引入 failure/httpclient；B4 引入 scheduler/calendar，并把
// scheduler 状态 / calendar_last_refresh 接进 /health。
package main

import (
	"context"
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

	"github.com/namelyzz/FundPilot/internal/platform/config"
	"github.com/namelyzz/FundPilot/internal/platform/db"
	"github.com/namelyzz/FundPilot/internal/platform/httpserver"
	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

const version = "0.1.0-b2"

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

	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pool, err := db.Open(openCtx, cfg.Database)
	cancel()
	if err != nil {
		lg.Error("db open failed", "err", err.Error())
		return 1
	}
	defer pool.Close()
	lg.Info("db pool ready", "max_open", cfg.Database.MaxOpen, "max_idle", cfg.Database.MaxIdle)

	srv := httpserver.New(httpserver.Options{
		Addr:         net.JoinHostPort("", strconv.Itoa(cfg.Server.Port)),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		Logger:       lg,
		HealthCheck:  makeHealthChecker(pool),
	})

	lg.Info("fundpilot starting",
		"version", version,
		"addr", srv.Addr,
		"log_level", cfg.Logger.Level,
		"log_format", cfg.Logger.Format,
	)

	if err := httpserver.Serve(ctx, srv, 10*time.Second); err != nil {
		lg.Error("http server exited", "err", err.Error())
		return 1
	}

	lg.Info("fundpilot stopped", "version", version)
	return 0
}

// makeHealthChecker 装配 /health 上各子系统的存活检查。
// scheduler / calendar_last_refresh 会在 B4 接入；当前先标 pending。
func makeHealthChecker(pool *pgxpool.Pool) httpserver.HealthChecker {
	return func(ctx context.Context) httpserver.HealthReport {
		report := httpserver.HealthReport{
			Status:    "ok",
			DB:        "ok",
			Scheduler: "pending(B4)",
			Version:   version,
		}
		pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		if err := db.Ping(pingCtx, pool); err != nil {
			report.DB = "down"
			report.Status = "degraded"
		}
		return report
	}
}
