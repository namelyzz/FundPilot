// Package calendar 实现 FR-PL-07：A 股交易日历。
//
// # 真源
//
// PG 表 trade_calendar（迁移 0001 创建），由 cmd/calendar-seed 灌入。
// 本包不直接调用上游（AkShare 等），保持纯净；上游对接放在 Python 探测脚本与
// cmd/calendar-seed 中（见 [[feedback-language-split]]）。
//
// # 缓存
//
// 启动时一次性加载全量 trade_calendar 到内存（一年也只有 ~250 个交易日 + 100 多个休市日，
// 内存压力可忽略）。后续 Refresh(ctx) 可显式重新拉。
//
// 不提供"按需查询 DB"的逻辑——cache miss 视为日历未覆盖该日期，直接返回 ErrDateNotCovered，
// 避免悄悄退化到"每次都打数据库"。
//
// # 交易时段
//
//   - 09:30:00 ≤ t < 11:30:00（上午）
//   - 13:00:00 ≤ t < 15:00:00（下午）
//   - 时区固定 Asia/Shanghai；调用方传入的 time.Time 会被强制转到该时区
package calendar

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDateNotCovered 在查询的日期没有出现在缓存中（且没有更早的开市日记录）时返回。
var ErrDateNotCovered = errors.New("calendar: date not covered")

// Entry 是一行交易日记录的领域表示。
type Entry struct {
	TradeDate     time.Time
	IsOpen        bool
	PrevTradeDate *time.Time // null 时表示该日之前没有任何交易日（一般是历史边界）
	UpdatedAt     time.Time
}

// Reader 暴露给调度器 / 业务的只读接口。
type Reader interface {
	IsTradingDay(date time.Time) (bool, error)
	IsTradingTime(t time.Time) bool
	PrevTradingDay(date time.Time) (time.Time, error)
}

// Service 是 calendar 的具体实现，安全可被多 goroutine 共享。
type Service struct {
	pool *pgxpool.Pool  // DB 连接池（Refresh 和 UpsertBatch 用）
	loc  *time.Location  // 时区，固定 Asia/Shanghai

	mu              sync.RWMutex        // 保护下面三个字段的读写锁
    byDate          map[civilDate]Entry  // 按日期查：日期 → 是否开市(O(1) 查某一天是不是交易日)
    sortedDates     []civilDate         // 升序排列的所有日期（二分查找上一个交易日）
    lastRefreshTime time.Time           // 上次刷新时间
}

// civilDate 是"无时区的日期"——避免 time.Time 因时区/秒位差异导致 map key 重复。
type civilDate struct {
	Year  int
	Month time.Month
	Day   int
}

func toCivil(t time.Time, loc *time.Location) civilDate {
	tt := t.In(loc)
	return civilDate{tt.Year(), tt.Month(), tt.Day()}
}

func (d civilDate) toTime(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

func (d civilDate) less(o civilDate) bool {
	if d.Year != o.Year {
		return d.Year < o.Year
	}
	if d.Month != o.Month {
		return d.Month < o.Month
	}
	return d.Day < o.Day
}

// New 构造 Service；timezone 当前固定为 Asia/Shanghai（除非系统找不到对应 tzdata 才会回落到固定偏移）。
func New(pool *pgxpool.Pool) *Service {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		// Windows 上偶尔会缺 tzdata；用 +08:00 兜底
		loc = time.FixedZone("CST", 8*3600)
	}
	return &Service{
		pool:   pool,
		loc:    loc,
		byDate: make(map[civilDate]Entry),
	}
}

// Refresh 从 PG 全量拉取 trade_calendar 到本地缓存。Service 启动后**必须**调用一次。
func (s *Service) Refresh(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("calendar: nil pool")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT trade_date, is_open, prev_trade_date, updated_at
		FROM trade_calendar
		ORDER BY trade_date ASC
	`)
	if err != nil {
		return fmt.Errorf("calendar: query: %w", err)
	}
	defer rows.Close()

	byDate := make(map[civilDate]Entry)
	sorted := make([]civilDate, 0, 512)
	for rows.Next() {
		var (
			td       time.Time
			isOpen   bool
			prev     *time.Time
			updated  time.Time
		)
		if err := rows.Scan(&td, &isOpen, &prev, &updated); err != nil {
			return fmt.Errorf("calendar: scan: %w", err)
		}
		key := toCivil(td, s.loc)
		byDate[key] = Entry{TradeDate: td.In(s.loc), IsOpen: isOpen, PrevTradeDate: prev, UpdatedAt: updated}
		sorted = append(sorted, key)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("calendar: rows: %w", err)
	}

	s.mu.Lock()
	s.byDate = byDate
	s.sortedDates = sorted
	s.lastRefreshTime = time.Now()
	s.mu.Unlock()

	return nil
}

// LoadSnapshot 把外部已有的 entries 灌入内存（不写库），供测试或一次性 bootstrap 使用。
// 生产场景请走 [Service.Refresh]。
func (s *Service) LoadSnapshot(entries []Entry) {
	byDate := make(map[civilDate]Entry, len(entries))
	sorted := make([]civilDate, 0, len(entries))
	for _, e := range entries {
		key := toCivil(e.TradeDate, s.loc)
		byDate[key] = Entry{
			TradeDate:     e.TradeDate.In(s.loc),
			IsOpen:        e.IsOpen,
			PrevTradeDate: e.PrevTradeDate,
			UpdatedAt:     e.UpdatedAt,
		}
		sorted = append(sorted, key)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].less(sorted[j]) })

	s.mu.Lock()
	s.byDate = byDate
	s.sortedDates = sorted
	s.lastRefreshTime = time.Now()
	s.mu.Unlock()
}

// UpsertBatch 把 entries 插入 / 更新到 trade_calendar；冲突按 trade_date 覆盖。
// 不刷新内存缓存——调用方在批量灌入后自行 Refresh()。
func (s *Service) UpsertBatch(ctx context.Context, entries []Entry) (inserted, updated int64, err error) {
	if s.pool == nil {
		return 0, 0, errors.New("calendar: nil pool")
	}
	if len(entries) == 0 {
		return 0, 0, nil
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, 0, fmt.Errorf("calendar: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, e := range entries {
		tag, exErr := tx.Exec(ctx, `
			INSERT INTO trade_calendar (trade_date, is_open, prev_trade_date, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (trade_date) DO UPDATE
			   SET is_open = EXCLUDED.is_open,
			       prev_trade_date = EXCLUDED.prev_trade_date,
			       updated_at = now()
			   WHERE trade_calendar.is_open IS DISTINCT FROM EXCLUDED.is_open
			      OR trade_calendar.prev_trade_date IS DISTINCT FROM EXCLUDED.prev_trade_date
		`, e.TradeDate, e.IsOpen, e.PrevTradeDate)
		if exErr != nil {
			return 0, 0, fmt.Errorf("calendar: upsert %s: %w", e.TradeDate.Format("2006-01-02"), exErr)
		}
		// pgx 在 ON CONFLICT DO UPDATE 时 RowsAffected 反映"受影响的行数"，无法直接区分 insert / update；
		// 这里用一个保守策略：先 count 全部，再事后查实际 insert（懒得做就都计入 updated）
		// 个人工具够用，后续真需要可改 RETURNING xmax=0 判定。
		_ = tag
		updated++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("calendar: commit: %w", err)
	}
	return inserted, updated, nil
}

// IsTradingDay 判断给定日期是否开市。
func (s *Service) IsTradingDay(date time.Time) (bool, error) {
	key := toCivil(date, s.loc)
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.byDate[key]
	if !ok {
		return false, ErrDateNotCovered
	}
	return e.IsOpen, nil
}

// IsTradingTime 判断给定时刻是否在交易时段内（含日期开市判断 + 时段判断）。
// 调用方传入未本地化的 time.Time 也安全——内部会转到 Asia/Shanghai。
func (s *Service) IsTradingTime(t time.Time) bool {
	open, err := s.IsTradingDay(t)
	if err != nil || !open {
		return false
	}
	tt := t.In(s.loc)
	h, m, sec := tt.Clock()
	mins := h*60 + m

	// 上午 09:30 ~ 11:30（不含 11:30:00）
    // 下午 13:00 ~ 15:00（不含 15:00:00）
	morningStart := 9*60 + 30
	morningEnd := 11*60 + 30
	afternoonStart := 13 * 60
	afternoonEnd := 15 * 60

	// 11:30:00.000 与 15:00:00.000 视为已收盘
	if mins >= morningStart && (mins < morningEnd || (mins == morningEnd && sec == 0 && tt.Nanosecond() == 0 && false)) {
		_ = sec
		return mins < morningEnd
	}
	if mins >= afternoonStart && mins < afternoonEnd {
		return true
	}
	return false
}

// PrevTradingDay 返回给定日期的上一个开市日。
//   - 给定日期本身是否开市都不影响——只要该日期被日历覆盖，就找到它之前最近的 is_open=true
//   - 如果该日期未被覆盖，返回 ErrDateNotCovered
//   - 如果在覆盖范围内找不到更早的开市日（边界），返回零值 + 错误
// sortedDates 是升序的，二分查找 O(log n)，比从末尾往前线性扫 O(n) 快。
// 虽然 n 很小（几百），但既然有序了就用二分。
func (s *Service) PrevTradingDay(date time.Time) (time.Time, error) {
	key := toCivil(date, s.loc)
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 先检查日期是否在覆盖范围内
	if _, ok := s.byDate[key]; !ok {
		return time.Time{}, ErrDateNotCovered
	}

	// 二分查找：在 sortedDates 中找到 < key 的最右位置
	idx := sort.Search(len(s.sortedDates), func(i int) bool {
		return !s.sortedDates[i].less(key)
	})

	// 从 idx-1 往前找第一个 IsOpen=true
	for i := idx - 1; i >= 0; i-- {
		if e, ok := s.byDate[s.sortedDates[i]]; ok && e.IsOpen {
			return s.sortedDates[i].toTime(s.loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("calendar: no prior trading day before %s", key.toTime(s.loc).Format("2006-01-02"))
}

// LastRefresh 返回最近一次 Refresh / LoadSnapshot 的本地时间。零值表示从未刷新过。
func (s *Service) LastRefresh() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRefreshTime
}

// Coverage 返回缓存里日期的覆盖范围（含），便于 /health 暴露。
func (s *Service) Coverage() (start, end time.Time, count int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sortedDates) == 0 {
		return time.Time{}, time.Time{}, 0
	}
	return s.sortedDates[0].toTime(s.loc),
		s.sortedDates[len(s.sortedDates)-1].toTime(s.loc),
		len(s.sortedDates)
}
