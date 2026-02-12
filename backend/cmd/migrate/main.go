// Command migrate 应用 / 回滚 goose SQL 迁移。
//
// 用法：
//
//	go run ./cmd/migrate up            # 应用全部待执行迁移
//	go run ./cmd/migrate down          # 回滚一步
//	go run ./cmd/migrate status        # 查看当前迁移状态
//	go run ./cmd/migrate redo          # 回滚最后一步并立即重做
//	go run ./cmd/migrate version       # 当前数据库版本
//
// DSN 解析顺序：
//  1. 环境变量 FUNDPILOT_DATABASE_DSN
//  2. 命令行 -dsn 标志
//  3. config.yaml（通过 -config 指定路径）
//
// 显式独立于服务进程，避免在 main.go 里自动跑迁移（FR-PL-04 约束）。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/stdlib" // 注册 database/sql 的 pgx driver
	"github.com/pressly/goose/v3"

	"github.com/namelyzz/FundPilot/internal/platform/config"
)

// 强制保留 driver import 不被 IDE 优化
var _ = stdlib.RegisterConnConfig

const migrationsDir = "migrations"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := run(); err != nil {
		log.Fatalf("migrate: %v", err)
	}
}

func run() error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dsnFlag := fs.String("dsn", "", "PostgreSQL DSN (overrides config & env)")
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	dir := fs.String("dir", migrationsDir, "migrations directory")

	if len(os.Args) < 2 {
		return fmt.Errorf("usage: migrate <command> [flags]\n  commands: up, down, status, redo, version, reset")
	}
	command := os.Args[1]
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	dsn, err := resolveDSN(*dsnFlag, *configPath)
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(context.Background()); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	if err := goose.RunContext(context.Background(), command, db, *dir, fs.Args()...); err != nil {
		return fmt.Errorf("goose %s: %w", command, err)
	}
	return nil
}

func resolveDSN(flagDSN, configPath string) (string, error) {
	if v := os.Getenv("FUNDPILOT_DATABASE_DSN"); v != "" {
		return v, nil
	}
	if flagDSN != "" {
		return flagDSN, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("resolve dsn: %w", err)
	}
	if cfg.Database.DSN == "" {
		return "", fmt.Errorf("resolve dsn: not found in env / flag / config")
	}
	return cfg.Database.DSN, nil
}
