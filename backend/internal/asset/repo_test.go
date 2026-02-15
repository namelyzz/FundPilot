package asset

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// testDSNEnv 指向一个允许销毁数据的 PG。未设置时所有用例 t.Skip——
// 让 `go test ./...` 在干净 clone 后也能跑通其它包；只有在 backend/.env 注入
// FUNDPILOT_TEST_DSN（或开发者手动 export）时才真正打 DB。
const testDSNEnv = "FUNDPILOT_TEST_DSN"

// truncateSQL 复位 positions 与 position_valuations。RESTART IDENTITY 让每个
// 用例从 id=1 开始，断言更稳。两张表都没 FK 互引，CASCADE 不必加。
const truncateSQL = `TRUNCATE TABLE positions, position_valuations RESTART IDENTITY`

// openTestPool 打开测试连接池并清空 asset 相关表。
// 每个用例的开头与 cleanup 都 truncate 一次，即使前一个用例失败也不串味。
func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping asset repo integration tests", testDSNEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open test pool: %v", err)
	}
	if _, err := pool.Exec(context.Background(), truncateSQL); err != nil {
		pool.Close()
		t.Fatalf("truncate before test: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), truncateSQL)
		pool.Close()
	})
	return pool
}

// mustDecimal 把字符串字面量转 decimal.Decimal，避免 NewFromFloat 的浮点精度问题。
func mustDecimal(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("decimal %q: %v", s, err)
	}
	return d
}

// samplePosition 是单测共用的"已经被 service 派生处理过"的 Position。
// 调用方按需覆盖字段（如 fund_code）。
func samplePosition(t *testing.T, fundCode string) *Position {
	t.Helper()
	amt := mustDecimal(t, "10000.00")
	profit := mustDecimal(t, "500.00")
	return &Position{
		FundCode:        fundCode,
		HoldingAmount:   amt,
		HoldingProfit:   profit,
		CostBasis:       amt.Sub(profit), // 9500.00
		EstimatedShares: decimal.NullDecimal{Decimal: mustDecimal(t, "8474.5763"), Valid: true},
		HoldingDays:     30,
		// pgx 把 DATE 列读回 time.Time 时使用 UTC 0 点；写入也按日期截断，
		// 用 UTC 0 点避免本地时区跨日漂移。
		HoldingStartDate: time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC),
		Source:           SourceManual,
	}
}

// ---- Create --------------------------------------------------------------

func TestPositionRepo_Create_HappyPath(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	in := samplePosition(t, "000001")
	got, err := repo.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if got.ID == 0 {
		t.Error("ID 未由 DB 回填")
	}
	if got.FundCode != "000001" {
		t.Errorf("FundCode: want 000001, got %q", got.FundCode)
	}
	if !got.HoldingAmount.Equal(in.HoldingAmount) {
		t.Errorf("HoldingAmount: want %s, got %s", in.HoldingAmount, got.HoldingAmount)
	}
	if !got.HoldingProfit.Equal(in.HoldingProfit) {
		t.Errorf("HoldingProfit: want %s, got %s", in.HoldingProfit, got.HoldingProfit)
	}
	if !got.CostBasis.Equal(in.CostBasis) {
		t.Errorf("CostBasis: want %s, got %s", in.CostBasis, got.CostBasis)
	}
	if !got.EstimatedShares.Valid {
		t.Error("EstimatedShares: want Valid=true")
	} else if !got.EstimatedShares.Decimal.Equal(in.EstimatedShares.Decimal) {
		t.Errorf("EstimatedShares: want %s, got %s", in.EstimatedShares.Decimal, got.EstimatedShares.Decimal)
	}
	if got.HoldingDays != in.HoldingDays {
		t.Errorf("HoldingDays: want %d, got %d", in.HoldingDays, got.HoldingDays)
	}
	if y, m, d := got.HoldingStartDate.Date(); y != 2026 || m != 4 || d != 27 {
		t.Errorf("HoldingStartDate: want 2026-04-27, got %04d-%02d-%02d", y, m, d)
	}
	if got.Source != SourceManual {
		t.Errorf("Source: want manual, got %q", got.Source)
	}
	if got.Version != 0 {
		t.Errorf("Version: want 0 on insert, got %d", got.Version)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt 未由 DB 回填")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt 未由 DB 回填")
	}
}

func TestPositionRepo_Create_DuplicateFundCode(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)
	ctx := context.Background()

	if _, err := repo.Create(ctx, samplePosition(t, "000002")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := repo.Create(ctx, samplePosition(t, "000002"))
	if !errors.Is(err, ErrPositionDuplicate) {
		t.Fatalf("second create: want ErrPositionDuplicate, got %v", err)
	}
}

func TestPositionRepo_Create_EstimatedSharesNullRoundtrip(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	in := samplePosition(t, "000003")
	in.EstimatedShares = decimal.NullDecimal{} // 显式 NULL（净值不可用场景）

	got, err := repo.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.EstimatedShares.Valid {
		t.Errorf("EstimatedShares: want Valid=false (NULL), got %+v", got.EstimatedShares)
	}
}

// ---- Get -----------------------------------------------------------------

func TestPositionRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	_, err := repo.GetByID(context.Background(), 999_999)
	if !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("want ErrPositionNotFound, got %v", err)
	}
}

func TestPositionRepo_GetByFundCode_HappyPath(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, samplePosition(t, "000004"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetByFundCode(ctx, "000004")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID: want %d, got %d", created.ID, got.ID)
	}
}

func TestPositionRepo_GetByFundCode_NotFound(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	_, err := repo.GetByFundCode(context.Background(), "999999")
	if !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("want ErrPositionNotFound, got %v", err)
	}
}

// ---- Update --------------------------------------------------------------

func TestPositionRepo_Update_Success_BumpsVersionAndUpdatedAt(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, samplePosition(t, "000005"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// DB now() 的精度可达 µs，但同一事务里 created_at == updated_at；
	// 显式停一会儿保证 update 后 updated_at 严格大于 created_at。
	time.Sleep(2 * time.Millisecond)

	// 模拟 service 行为：拿到既有 Position → 改 HoldingAmount → 重算 CostBasis
	created.HoldingAmount = mustDecimal(t, "12000.00")
	created.CostBasis = created.HoldingAmount.Sub(created.HoldingProfit) // 11500.00

	updated, err := repo.Update(ctx, created)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated.HoldingAmount.Equal(mustDecimal(t, "12000.00")) {
		t.Errorf("HoldingAmount: want 12000.00, got %s", updated.HoldingAmount)
	}
	if !updated.CostBasis.Equal(mustDecimal(t, "11500.00")) {
		t.Errorf("CostBasis: want 11500.00, got %s", updated.CostBasis)
	}
	if updated.Version != 1 {
		t.Errorf("Version: want 1 after first update, got %d", updated.Version)
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Errorf("UpdatedAt (%v) 应严格晚于 CreatedAt (%v)", updated.UpdatedAt, updated.CreatedAt)
	}
}

func TestPositionRepo_Update_NotFound(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	ghost := samplePosition(t, "000006")
	ghost.ID = 999_999
	ghost.Version = 0

	_, err := repo.Update(context.Background(), ghost)
	if !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("want ErrPositionNotFound, got %v", err)
	}
}

func TestPositionRepo_Update_VersionConflict(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, samplePosition(t, "000007"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 模拟"被其它请求抢先改过"：先做一次合法 update，让 DB 版本 = 1
	created.HoldingAmount = mustDecimal(t, "11000.00")
	created.CostBasis = mustDecimal(t, "10500.00")
	if _, err := repo.Update(ctx, created); err != nil {
		t.Fatalf("first update: %v", err)
	}

	// 现在我们拿一个 version 已过期的副本去改
	stale := samplePosition(t, "000007")
	stale.ID = created.ID
	stale.Version = 0 // 故意用旧版本
	stale.HoldingAmount = mustDecimal(t, "99999.00")
	stale.CostBasis = mustDecimal(t, "99499.00")

	_, err = repo.Update(ctx, stale)
	if !errors.Is(err, ErrPositionVersionConflict) {
		t.Fatalf("want ErrPositionVersionConflict, got %v", err)
	}

	// 关键：行的数据不能被冲突的 UPDATE 改动
	fresh, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get after conflict: %v", err)
	}
	if !fresh.HoldingAmount.Equal(mustDecimal(t, "11000.00")) {
		t.Errorf("冲突未回滚？HoldingAmount got %s, want 11000.00", fresh.HoldingAmount)
	}
	if fresh.Version != 1 {
		t.Errorf("冲突误增 version？got %d, want 1", fresh.Version)
	}
}

// ---- Delete --------------------------------------------------------------

func TestPositionRepo_Delete_HappyPath(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, samplePosition(t, "000008"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, created.ID); !errors.Is(err, ErrPositionNotFound) {
		t.Errorf("删除后 GetByID 应返回 ErrPositionNotFound, got %v", err)
	}
}

func TestPositionRepo_Delete_NotFound(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	if err := repo.Delete(context.Background(), 999_999); !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("want ErrPositionNotFound, got %v", err)
	}
}

// ---- List ----------------------------------------------------------------

func TestPositionRepo_List_OrderedAscByID(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)
	ctx := context.Background()

	// 故意按非升序的 fund_code 顺序插入，验证 List 仍按 id 升序返回
	codes := []string{"000020", "000010", "000030"}
	for _, c := range codes {
		if _, err := repo.Create(ctx, samplePosition(t, c)); err != nil {
			t.Fatalf("create %s: %v", c, err)
		}
	}

	got, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len: want 3, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Errorf("非升序：got[%d].ID=%d <= got[%d].ID=%d",
				i, got[i].ID, i-1, got[i-1].ID)
		}
	}
	// 插入顺序与 codes 对齐：BIGSERIAL 单调递增
	wantCodeOrder := []string{"000020", "000010", "000030"}
	for i, c := range wantCodeOrder {
		if got[i].FundCode != c {
			t.Errorf("got[%d].FundCode: want %s, got %s", i, c, got[i].FundCode)
		}
	}
}

func TestPositionRepo_List_EmptyTable(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	got, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空表 List 应返回空切片，got %d 条", len(got))
	}
}

// ---- Decimal roundtrip ---------------------------------------------------

func TestPositionRepo_DecimalRoundtrip(t *testing.T) {
	pool := openTestPool(t)
	repo := NewPositionRepo(pool)

	// 覆盖几种容易踩坑的值：
	//   - 正常两位小数 12345.67
	//   - 负数与尾随零 -89.10（DECIMAL 不会被截成 -89.1，但 Decimal.String 默认会，
	//     所以断言用 Equal 比数值，不比字面量）
	//   - 较高精度的 estimated_shares（DECIMAL(20,4)）
	in := &Position{
		FundCode:         "000099",
		HoldingAmount:    mustDecimal(t, "12345.67"),
		HoldingProfit:    mustDecimal(t, "-89.10"),
		CostBasis:        mustDecimal(t, "12434.77"),
		EstimatedShares:  decimal.NullDecimal{Decimal: mustDecimal(t, "10456.7891"), Valid: true},
		HoldingDays:      0,
		HoldingStartDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Source:           SourceManual,
	}

	got, err := repo.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cases := []struct {
		name string
		want decimal.Decimal
		got  decimal.Decimal
	}{
		{"holding_amount", in.HoldingAmount, got.HoldingAmount},
		{"holding_profit", in.HoldingProfit, got.HoldingProfit},
		{"cost_basis", in.CostBasis, got.CostBasis},
		{"estimated_shares", in.EstimatedShares.Decimal, got.EstimatedShares.Decimal},
	}
	for _, c := range cases {
		if !c.got.Equal(c.want) {
			t.Errorf("%s: want %s, got %s", c.name, c.want, c.got)
		}
	}
}
