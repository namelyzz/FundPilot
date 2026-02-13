// Command calendar-seed 把 trade_calendar CSV 入库（FR-PL-07 兜底/初始数据路径）。
//
// 用法：
//
//	go run ./cmd/calendar-seed -input cal.csv         # 读文件
//	python -m data_probe.probe_trade_calendar --csv | go run ./cmd/calendar-seed
//
// CSV 列：trade_date,is_open,prev_trade_date
//   - trade_date：YYYY-MM-DD
//   - is_open：true / false（也接受 1/0、yes/no）
//   - prev_trade_date：YYYY-MM-DD，或空字符串
//
// 与 cmd/migrate 一样保持显式调用——不在服务启动时自动 seed，避免误覆盖。
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/namelyzz/FundPilot/internal/platform/calendar"
	"github.com/namelyzz/FundPilot/internal/platform/config"
	"github.com/namelyzz/FundPilot/internal/platform/db"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := run(); err != nil {
		log.Fatalf("calendar-seed: %v", err)
	}
}

func run() error {
	fs := flag.NewFlagSet("calendar-seed", flag.ExitOnError)
	input := fs.String("input", "", "CSV 文件路径（留空读 stdin）")
	configPath := fs.String("config", "", "config.yaml 路径（解析 DSN 用，可选）")
	dsnFlag := fs.String("dsn", "", "PostgreSQL DSN（覆盖配置 / 环境变量）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	entries, err := readEntries(*input)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("empty input")
	}

	dsn, err := resolveDSN(*dsnFlag, *configPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, config.Database{DSN: dsn, MaxOpen: 4, MaxIdle: 1})
	if err != nil {
		return err
	}
	defer pool.Close()

	svc := calendar.New(pool)
	inserted, updated, err := svc.UpsertBatch(ctx, entries)
	if err != nil {
		return err
	}
	log.Printf("ok: %d entries processed (inserted~%d / upserted~%d)", len(entries), inserted, updated)
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
		return "", errors.New("resolve dsn: not found in env / flag / config")
	}
	return cfg.Database.DSN, nil
}

// readEntries 从文件或 stdin 读 CSV → []calendar.Entry。
func readEntries(path string) ([]calendar.Entry, error) {
	var r io.Reader
	if path == "" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %q: %w", path, err)
		}
		defer f.Close()
		r = f
	}
	return parseCSV(r)
}

func parseCSV(r io.Reader) ([]calendar.Entry, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // 容忍尾部空字段
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("csv has no rows")
	}

	header := rows[0]
	idxTradeDate, idxIsOpen, idxPrev := -1, -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "trade_date":
			idxTradeDate = i
		case "is_open":
			idxIsOpen = i
		case "prev_trade_date":
			idxPrev = i
		}
	}
	if idxTradeDate < 0 || idxIsOpen < 0 {
		return nil, fmt.Errorf("csv missing required column trade_date / is_open; got %v", header)
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}

	out := make([]calendar.Entry, 0, len(rows)-1)
	for li, row := range rows[1:] {
		if len(row) <= idxTradeDate || len(row) <= idxIsOpen {
			return nil, fmt.Errorf("csv line %d: too few columns", li+2)
		}
		td, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(row[idxTradeDate]), loc)
		if err != nil {
			return nil, fmt.Errorf("csv line %d trade_date: %w", li+2, err)
		}
		isOpen, err := parseBool(row[idxIsOpen])
		if err != nil {
			return nil, fmt.Errorf("csv line %d is_open: %w", li+2, err)
		}
		var prev *time.Time
		if idxPrev >= 0 && idxPrev < len(row) {
			pv := strings.TrimSpace(row[idxPrev])
			if pv != "" {
				p, err := time.ParseInLocation("2006-01-02", pv, loc)
				if err != nil {
					return nil, fmt.Errorf("csv line %d prev_trade_date: %w", li+2, err)
				}
				prev = &p
			}
		}
		out = append(out, calendar.Entry{TradeDate: td, IsOpen: isOpen, PrevTradeDate: prev})
	}
	return out, nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "y", "t":
		return true, nil
	case "false", "0", "no", "n", "f", "":
		return false, nil
	}
	return false, fmt.Errorf("invalid bool %q", s)
}
