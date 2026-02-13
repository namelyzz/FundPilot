package calendar

import (
	"errors"
	"testing"
	"time"
)

func dateAt(y int, m time.Month, d, h, min int) time.Time {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return time.Date(y, m, d, h, min, 0, 0, loc)
}

func ptrTime(t time.Time) *time.Time { return &t }

// sampleEntries：覆盖 2026-02-09(周一)~2026-02-13(周五)，含一个虚构的节假日 2026-02-11
func sampleEntries() []Entry {
	mon := dateAt(2026, 2, 9, 0, 0)
	tue := dateAt(2026, 2, 10, 0, 0)
	wed := dateAt(2026, 2, 11, 0, 0) // 假节日
	thu := dateAt(2026, 2, 12, 0, 0)
	fri := dateAt(2026, 2, 13, 0, 0)
	sat := dateAt(2026, 2, 14, 0, 0)
	return []Entry{
		{TradeDate: mon, IsOpen: true, PrevTradeDate: nil},
		{TradeDate: tue, IsOpen: true, PrevTradeDate: ptrTime(mon)},
		{TradeDate: wed, IsOpen: false, PrevTradeDate: ptrTime(tue)},
		{TradeDate: thu, IsOpen: true, PrevTradeDate: ptrTime(tue)},
		{TradeDate: fri, IsOpen: true, PrevTradeDate: ptrTime(thu)},
		{TradeDate: sat, IsOpen: false, PrevTradeDate: ptrTime(fri)},
	}
}

func newLoaded() *Service {
	s := New(nil)
	s.LoadSnapshot(sampleEntries())
	return s
}

func TestIsTradingDay_MapsCorrectly(t *testing.T) {
	s := newLoaded()

	cases := []struct {
		date time.Time
		want bool
	}{
		{dateAt(2026, 2, 9, 14, 0), true},
		{dateAt(2026, 2, 11, 10, 0), false}, // 节假日
		{dateAt(2026, 2, 14, 10, 0), false}, // 周六
	}
	for _, c := range cases {
		got, err := s.IsTradingDay(c.date)
		if err != nil {
			t.Errorf("date %v: unexpected err %v", c.date, err)
			continue
		}
		if got != c.want {
			t.Errorf("date %v: got %v want %v", c.date, got, c.want)
		}
	}
}

func TestIsTradingDay_OutOfRangeReturnsErr(t *testing.T) {
	s := newLoaded()
	_, err := s.IsTradingDay(dateAt(2030, 1, 1, 0, 0))
	if !errors.Is(err, ErrDateNotCovered) {
		t.Fatalf("expected ErrDateNotCovered, got %v", err)
	}
}

func TestIsTradingTime_Sessions(t *testing.T) {
	s := newLoaded()

	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"premarket", dateAt(2026, 2, 9, 9, 0), false},
		{"open_morning", dateAt(2026, 2, 9, 9, 30), true},
		{"mid_morning", dateAt(2026, 2, 9, 10, 0), true},
		{"morning_close_edge", dateAt(2026, 2, 9, 11, 30), false},
		{"lunch_break", dateAt(2026, 2, 9, 12, 0), false},
		{"open_afternoon", dateAt(2026, 2, 9, 13, 0), true},
		{"mid_afternoon", dateAt(2026, 2, 9, 14, 30), true},
		{"close_edge", dateAt(2026, 2, 9, 15, 0), false},
		{"after_close", dateAt(2026, 2, 9, 16, 0), false},
		{"holiday_during_hours", dateAt(2026, 2, 11, 10, 0), false},
		{"weekend_during_hours", dateAt(2026, 2, 14, 10, 0), false},
	}
	for _, c := range cases {
		if got := s.IsTradingTime(c.t); got != c.want {
			t.Errorf("%s (%v): got %v want %v", c.name, c.t, got, c.want)
		}
	}
}

func TestIsTradingTime_TimezoneInputNormalized(t *testing.T) {
	s := newLoaded()
	// 一个 UTC 时间 = Asia/Shanghai 2026-02-09 10:00（开盘期间）
	tUTC := time.Date(2026, 2, 9, 2, 0, 0, 0, time.UTC)
	if !s.IsTradingTime(tUTC) {
		t.Error("UTC input that maps to Shanghai 10:00 should be trading time")
	}
}

func TestPrevTradingDay_FindsCorrectAnchor(t *testing.T) {
	s := newLoaded()

	cases := []struct {
		from time.Time
		want time.Time
	}{
		{dateAt(2026, 2, 13, 0, 0), dateAt(2026, 2, 12, 0, 0)}, // Fri ← Thu
		{dateAt(2026, 2, 12, 0, 0), dateAt(2026, 2, 10, 0, 0)}, // Thu ← Tue（跳过节日）
		{dateAt(2026, 2, 11, 0, 0), dateAt(2026, 2, 10, 0, 0)}, // 节日 ← 周二
		{dateAt(2026, 2, 14, 0, 0), dateAt(2026, 2, 13, 0, 0)}, // 周六 ← 周五
	}
	for _, c := range cases {
		got, err := s.PrevTradingDay(c.from)
		if err != nil {
			t.Errorf("from %v: %v", c.from, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("from %v: got %v want %v", c.from, got, c.want)
		}
	}
}

func TestPrevTradingDay_LeftBoundary(t *testing.T) {
	s := newLoaded()
	// 2026-02-09 是数据范围里第一个开市日，再之前没有可参考的开市日
	_, err := s.PrevTradingDay(dateAt(2026, 2, 9, 0, 0))
	if err == nil {
		t.Fatal("expected boundary error")
	}
	if errors.Is(err, ErrDateNotCovered) {
		t.Errorf("boundary error should be distinct from ErrDateNotCovered, got %v", err)
	}
}

func TestCoverageAndLastRefresh(t *testing.T) {
	s := newLoaded()
	start, end, count := s.Coverage()
	if count != 6 {
		t.Errorf("count = %d", count)
	}
	if !start.Equal(dateAt(2026, 2, 9, 0, 0)) || !end.Equal(dateAt(2026, 2, 14, 0, 0)) {
		t.Errorf("range = %v ~ %v", start, end)
	}
	if s.LastRefresh().IsZero() {
		t.Error("LastRefresh should be set after LoadSnapshot")
	}
}

func TestEmptyService_QueriesReturnErrors(t *testing.T) {
	s := New(nil)
	if _, err := s.IsTradingDay(dateAt(2026, 2, 9, 0, 0)); err == nil {
		t.Error("empty service should ErrDateNotCovered")
	}
	if s.IsTradingTime(dateAt(2026, 2, 9, 10, 0)) {
		t.Error("empty service IsTradingTime should be false")
	}
	if _, _, c := s.Coverage(); c != 0 {
		t.Errorf("empty service Coverage count = %d", c)
	}
}
