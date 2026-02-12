// Package db 实现 FR-PL-04：进程级单例 pgxpool 连接池。
//
// 注意事项：
//   - V0.1 阶段仅做"开池/探活/关池"，不对外暴露 query/exec 包装；业务层直接
//     使用 *pgxpool.Pool 提供的接口，保持低抽象。
//   - 启动时**不**自动跑迁移（REQ-01 FR-PL-04），由 make migrate 显式触发。
//   - max_idle 通过 pgxpool 的 MinConns 表达；max_open 对应 MaxConns。
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/namelyzz/FundPilot/internal/platform/config"
)

// Open 解析 DSN、应用池配置、建立连接池，并做一次握手探活。
//
// 调用方拿到 *pgxpool.Pool 后必须负责 Close（通常在 main 的 defer 里）。
func Open(ctx context.Context, cfg config.Database) (*pgxpool.Pool, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("db: empty dsn")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: parse dsn: %w", err)
	}

	if cfg.MaxOpen > 0 {
		poolCfg.MaxConns = int32(cfg.MaxOpen)
	}
	if cfg.MaxIdle > 0 {
		poolCfg.MinConns = int32(cfg.MaxIdle)
	}
	// 合理基线：空闲连接最长保活 30 分钟，超过强制重连，规避 PG 端 FIN
	poolCfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: new pool: %w", err)
	}

	if err := Ping(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Ping 执行一次握手；超时由 ctx 控制（推荐 1–2s）。
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("db: nil pool")
	}
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("db: ping: %w", err)
	}
	return nil
}
