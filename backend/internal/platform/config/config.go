// Package config 实现 FR-PL-02：一次性加载配置，全局只读。
//
// 加载顺序：
//  1. 内置默认值（与 config.example.yaml 对齐）
//  2. 显式传入的 yaml 文件（不存在则忽略，依赖默认值 + 环境变量）
//  3. 环境变量覆盖：前缀 FUNDPILOT_，分段用 "_" 拼接，如
//     FUNDPILOT_SERVER_PORT=9090, FUNDPILOT_DATABASE_DSN=postgres://...
//
// 注意：业务代码不得直接访问 viper / os.Getenv；一律通过 *Config。
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 是进程唯一的配置只读视图。
type Config struct {
	Server     Server     `mapstructure:"server"`
	Database   Database   `mapstructure:"database"`
	Valuation  Valuation  `mapstructure:"valuation"`
	MarketData MarketData `mapstructure:"market_data"`
	Logger     Logger     `mapstructure:"logger"`
}

type Server struct {
	Port         int           `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type Database struct {
	DSN     string `mapstructure:"dsn"`
	MaxOpen int    `mapstructure:"max_open"`
	MaxIdle int    `mapstructure:"max_idle"`
}

type Valuation struct {
	RefreshInterval  time.Duration `mapstructure:"refresh_interval"`
	TradingHoursOnly bool          `mapstructure:"trading_hours_only"`
}

type MarketData struct {
	PrimarySource   string        `mapstructure:"primary_source"`
	FallbackSources []string      `mapstructure:"fallback_sources"`
	RequestTimeout  time.Duration `mapstructure:"request_timeout"`
	MaxRetries      int           `mapstructure:"max_retries"`
}

type Logger struct {
	Level  string `mapstructure:"level"`  // debug/info/warn/error
	Format string `mapstructure:"format"` // json/console
}

// Load 读取配置。path 为空时只用默认值 + 环境变量。
func Load(path string) (*Config, error) {
	v := viper.New()
	applyDefaults(v)

	v.SetEnvPrefix("FUNDPILOT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			// 文件显式给了但读不到，是真错误（拼写错误 / 权限）
			if _, statErr := os.Stat(path); statErr != nil {
				return nil, fmt.Errorf("config: open %q: %w", path, statErr)
			}
			return nil, fmt.Errorf("config: parse %q: %w", path, err)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", 10*time.Second)
	v.SetDefault("server.write_timeout", 30*time.Second)

	v.SetDefault("database.dsn", "postgres://fundpilot:fundpilot@localhost:5432/fundpilot?sslmode=disable")
	v.SetDefault("database.max_open", 20)
	v.SetDefault("database.max_idle", 5)

	v.SetDefault("valuation.refresh_interval", 5*time.Minute)
	v.SetDefault("valuation.trading_hours_only", true)

	v.SetDefault("market_data.primary_source", "eastmoney")
	v.SetDefault("market_data.fallback_sources", []string{"akshare"})
	v.SetDefault("market_data.request_timeout", 5*time.Second)
	v.SetDefault("market_data.max_retries", 2)

	v.SetDefault("logger.level", "info")
	v.SetDefault("logger.format", "json")
}

func (c *Config) validate() error {
	var errs []string
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Sprintf("server.port out of range: %d", c.Server.Port))
	}
	if c.Database.DSN == "" {
		errs = append(errs, "database.dsn is empty")
	}
	if c.Database.MaxOpen < c.Database.MaxIdle {
		errs = append(errs, fmt.Sprintf("database.max_open(%d) < max_idle(%d)", c.Database.MaxOpen, c.Database.MaxIdle))
	}
	switch strings.ToLower(c.Logger.Level) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf("logger.level invalid: %q", c.Logger.Level))
	}
	switch strings.ToLower(c.Logger.Format) {
	case "json", "console":
	default:
		errs = append(errs, fmt.Sprintf("logger.format invalid: %q", c.Logger.Format))
	}
	if len(errs) > 0 {
		return errors.New("config: " + strings.Join(errs, "; "))
	}
	return nil
}
