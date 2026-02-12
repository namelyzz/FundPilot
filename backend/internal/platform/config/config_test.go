package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad_DefaultsOnly(t *testing.T) {
	clearEnv(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("server.port default = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Valuation.RefreshInterval != 5*time.Minute {
		t.Errorf("valuation.refresh_interval default = %v, want 5m", cfg.Valuation.RefreshInterval)
	}
	if cfg.Logger.Level != "info" || cfg.Logger.Format != "json" {
		t.Errorf("logger defaults wrong: %+v", cfg.Logger)
	}
	if len(cfg.MarketData.FallbackSources) == 0 {
		t.Errorf("market_data.fallback_sources should default to non-empty")
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUNDPILOT_SERVER_PORT", "9090")
	t.Setenv("FUNDPILOT_DATABASE_DSN", "postgres://u:p@example:5432/db?sslmode=disable")
	t.Setenv("FUNDPILOT_LOGGER_LEVEL", "debug")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("env override server.port = %d, want 9090", cfg.Server.Port)
	}
	if !strings.Contains(cfg.Database.DSN, "example:5432") {
		t.Errorf("env override database.dsn missed: %q", cfg.Database.DSN)
	}
	if cfg.Logger.Level != "debug" {
		t.Errorf("env override logger.level = %q, want debug", cfg.Logger.Level)
	}
}

func TestLoad_YamlFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const body = `
server:
  port: 7070
  read_timeout: 3s
  write_timeout: 7s
database:
  dsn: "postgres://u:p@x:5432/d?sslmode=disable"
  max_open: 10
  max_idle: 2
valuation:
  refresh_interval: 1m
  trading_hours_only: false
market_data:
  primary_source: tencent
  fallback_sources: [sina, akshare]
  request_timeout: 8s
  max_retries: 1
logger:
  level: warn
  format: console
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 7070 || cfg.Server.ReadTimeout != 3*time.Second {
		t.Errorf("yaml not applied to server: %+v", cfg.Server)
	}
	if cfg.MarketData.PrimarySource != "tencent" || len(cfg.MarketData.FallbackSources) != 2 {
		t.Errorf("yaml not applied to market_data: %+v", cfg.MarketData)
	}
	if cfg.Logger.Level != "warn" || cfg.Logger.Format != "console" {
		t.Errorf("yaml not applied to logger: %+v", cfg.Logger)
	}
}

func TestLoad_EnvOverridesYaml(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 7070\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FUNDPILOT_SERVER_PORT", "9999")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("env should beat yaml, got port=%d", cfg.Server.Port)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	clearEnv(t)
	_, err := Load("/definitely/not/here/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing explicit file")
	}
}

func TestLoad_ValidationCatchesBadPort(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUNDPILOT_SERVER_PORT", "70000")
	if _, err := Load(""); err == nil {
		t.Fatal("expected validation error for bad port")
	}
}

func TestLoad_ValidationCatchesBadLevel(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUNDPILOT_LOGGER_LEVEL", "trace")
	if _, err := Load(""); err == nil {
		t.Fatal("expected validation error for bad log level")
	}
}

// clearEnv 在测试开始前清掉所有可能干扰的 FUNDPILOT_* 环境变量。
// 注意：t.Setenv 在结束时只恢复它"明确设过"的那些；如果当前 shell 本来就带着
// FUNDPILOT_SERVER_PORT，DefaultsOnly 用例会被污染。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "FUNDPILOT_") {
			eq := strings.IndexByte(kv, '=')
			t.Setenv(kv[:eq], "") // 清空，t 会在结束时恢复
		}
	}
}
