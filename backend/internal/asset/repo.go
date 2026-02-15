package asset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// PositionRepo 封装 positions 表的存取，不含任何业务派生逻辑。
//
// 派生字段（CostBasis / EstimatedShares）由 service 在调用 Create / Update 前算好
// 并填到 *Position 上；repo 收到什么写什么，未来反推口径调整不影响 SQL。
//
// 并发 / 一致性：单条 SQL 操作，本身原子；service 若需要"读 → 改 → 写"原子性
// （Update 路径就是），应自行 BeginTx 或加乐观锁。V0.1 单用户，先不引入。
type PositionRepo struct {
	pool *pgxpool.Pool
}

// NewPositionRepo 构造仓储；pool 不允许为 nil（运行时缺少连接池属于装配错误，
// 让后续 Query 直接 panic 比静默返回 nil 更早暴露问题）。
func NewPositionRepo(pool *pgxpool.Pool) *PositionRepo {
	return &PositionRepo{pool: pool}
}

// positionColumns 是所有读路径与 RETURNING 共用的列清单，集中维护避免漂移。
// 顺序与 scanPosition 内的 Scan dest 一一对应，新增列时两处一起改。
const positionColumns = `id, fund_code,
	holding_amount, holding_profit, cost_basis, estimated_shares,
	holding_days, holding_start_date,
	source, version, created_at, updated_at`

// pgUniqueViolation 是 PG 的 unique_violation SQLSTATE；映射到 ErrPositionDuplicate。
const pgUniqueViolation = "23505"

// ---- CRUD ---------------------------------------------------------------

// Create 插入一条持仓。p 必须由 service 填充完整（含派生字段），repo 不补默认值。
// id / created_at / updated_at 由 DB 生成并通过 RETURNING 回填到返回的 *Position。
// fund_code 唯一冲突 → ErrPositionDuplicate；其它错误 wrap 后透传。
func (r *PositionRepo) Create(ctx context.Context, p *Position) (*Position, error) {
	const q = `
		INSERT INTO positions (
			fund_code, holding_amount, holding_profit, cost_basis,
			estimated_shares, holding_days, holding_start_date, source
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + positionColumns

	row := r.pool.QueryRow(ctx, q,
		p.FundCode,
		numericFromDecimal(p.HoldingAmount),
		numericFromDecimal(p.HoldingProfit),
		numericFromDecimal(p.CostBasis),
		numericFromNullDecimal(p.EstimatedShares),
		p.HoldingDays,
		p.HoldingStartDate,
		string(p.Source),
	)

	created, err := scanPosition(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return nil, ErrPositionDuplicate
		}
		return nil, fmt.Errorf("asset: insert position: %w", err)
	}
	return created, nil
}

// GetByID 按主键取一条；不存在 → ErrPositionNotFound。
func (r *PositionRepo) GetByID(ctx context.Context, id int64) (*Position, error) {
	const q = `SELECT ` + positionColumns + ` FROM positions WHERE id = $1`
	return r.queryOne(ctx, q, id)
}

// GetByFundCode 按 fund_code 取一条；不存在 → ErrPositionNotFound。
// 供 service.Create 在 INSERT 前做存在性预检（用户友好错误码），UNIQUE 约束仍是
// 最终防线（并发场景下预检会漏，Create 路径再兜一次）。
func (r *PositionRepo) GetByFundCode(ctx context.Context, fundCode string) (*Position, error) {
	const q = `SELECT ` + positionColumns + ` FROM positions WHERE fund_code = $1`
	return r.queryOne(ctx, q, fundCode)
}

// Update 用 p 的当前字段值整体覆盖 id == p.ID 的行；service 调用前必须已经把
// PositionPatch 合并到本地副本并重算派生字段。fund_code / created_at 不在 SET 中
// （前者用户不可改，后者由 DB 维护）。updated_at 重写为 now()；version 自增。
//
// 乐观锁：WHERE 同时校验 id 与 version，避免并发 PATCH 互相覆盖派生字段
// （read-modify-write 之间另一请求修改了同一行）。
// UPDATE 0 命中时需再 SELECT 一次 EXISTS 区分两种原因：
//   - 行不存在            → ErrPositionNotFound
//   - 行存在但 version 不一致 → ErrPositionVersionConflict
// 服务端不重试，直接把 conflict 抛到 service 由 HTTP 层返 409。
func (r *PositionRepo) Update(ctx context.Context, p *Position) (*Position, error) {
	const q = `
		UPDATE positions SET
			holding_amount     = $2,
			holding_profit     = $3,
			cost_basis         = $4,
			estimated_shares   = $5,
			holding_days       = $6,
			holding_start_date = $7,
			source             = $8,
			version            = version + 1,
			updated_at         = now()
		WHERE id = $1 AND version = $9
		RETURNING ` + positionColumns

	row := r.pool.QueryRow(ctx, q,
		p.ID,
		numericFromDecimal(p.HoldingAmount),
		numericFromDecimal(p.HoldingProfit),
		numericFromDecimal(p.CostBasis),
		numericFromNullDecimal(p.EstimatedShares),
		p.HoldingDays,
		p.HoldingStartDate,
		string(p.Source),
		p.Version,
	)

	updated, err := scanPosition(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, r.classifyUpdateMiss(ctx, p.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("asset: update position: %w", err)
	}
	return updated, nil
}

// classifyUpdateMiss 在 UPDATE 0 命中时区分"行不存在"与"version 冲突"。
// 额外一次 SELECT EXISTS 换错误码精度；V0.1 数据量下成本可忽略。
func (r *PositionRepo) classifyUpdateMiss(ctx context.Context, id int64) error {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM positions WHERE id = $1)`, id,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("asset: classify update miss: %w", err)
	}
	if !exists {
		return ErrPositionNotFound
	}
	return ErrPositionVersionConflict
}

// Delete 物理删除持仓行。position_valuations 历史快照按 spec FR-AS-03 保留
// （迁移 0002 故意不设 FK，详见迁移内注释）。命中 0 行 → ErrPositionNotFound。
func (r *PositionRepo) Delete(ctx context.Context, id int64) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM positions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("asset: delete position: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrPositionNotFound
	}
	return nil
}

// List 返回所有持仓，按 id 升序。V0.1 spec §FR-AS-04 明确不分页（< 50 条）。
// 无数据时返回 nil 切片（不视为错误）。
func (r *PositionRepo) List(ctx context.Context) ([]Position, error) {
	const q = `SELECT ` + positionColumns + ` FROM positions ORDER BY id ASC`

	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("asset: list positions: %w", err)
	}
	defer rows.Close()

	var out []Position
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, fmt.Errorf("asset: scan position: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("asset: list rows: %w", err)
	}
	return out, nil
}

// ---- 内部 helpers --------------------------------------------------------

// queryOne 包装 "SELECT ... WHERE x = $1" 的 not-found 错误映射，给 GetByID /
// GetByFundCode 共用。
func (r *PositionRepo) queryOne(ctx context.Context, q string, arg any) (*Position, error) {
	row := r.pool.QueryRow(ctx, q, arg)
	p, err := scanPosition(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPositionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("asset: query position: %w", err)
	}
	return p, nil
}

// rowScanner 同时被 pgx.Row（QueryRow 返回）和 pgx.Rows（Query → Next 迭代）满足，
// 让 scanPosition 在单行与多行场景下共用一份扫描逻辑。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanPosition 把当前行扫到 *Position；列顺序必须与 positionColumns 严格一致。
// 数值列先扫到 pgtype.Numeric，再桥接到 decimal.Decimal / decimal.NullDecimal，
// 因为 pgx/v5 没有为 shopspring/decimal 提供原生 codec。
func scanPosition(s rowScanner) (*Position, error) {
	var (
		p                                                Position
		holdingAmount, holdingProfit, costBasis, shares  pgtype.Numeric
		holdingStartDate                                 time.Time
		src                                              string
	)

	if err := s.Scan(
		&p.ID, &p.FundCode,
		&holdingAmount, &holdingProfit, &costBasis, &shares,
		&p.HoldingDays, &holdingStartDate,
		&src, &p.Version, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}

	var err error
	if p.HoldingAmount, err = decimalFromNumeric(holdingAmount); err != nil {
		return nil, fmt.Errorf("holding_amount: %w", err)
	}
	if p.HoldingProfit, err = decimalFromNumeric(holdingProfit); err != nil {
		return nil, fmt.Errorf("holding_profit: %w", err)
	}
	if p.CostBasis, err = decimalFromNumeric(costBasis); err != nil {
		return nil, fmt.Errorf("cost_basis: %w", err)
	}
	if p.EstimatedShares, err = nullDecimalFromNumeric(shares); err != nil {
		return nil, fmt.Errorf("estimated_shares: %w", err)
	}
	p.HoldingStartDate = holdingStartDate
	p.Source = Source(src)
	return &p, nil
}

// ---- decimal ↔ pgtype.Numeric 桥接 ---------------------------------------
//
// 两边都用 "Int × 10^Exp" 表示；不经过字符串/float，避免精度漂移。
// Coefficient() 和 NewFromBigInt() 都做 deep copy，桥接函数不会让两侧共享内部 big.Int。

// errNullNumericForNonNullColumn 用于"列声明为 NOT NULL 但 pgx 扫出 NULL"的不可达分支，
// 真触发说明 schema 与代码不同步——保留为内部错误便于日志识别。
var errNullNumericForNonNullColumn = errors.New("unexpected SQL NULL for non-null column")

// errNonFiniteNumeric 用于 NaN / ±Infinity；当前 schema 不允许写入这类值，但 pgx
// 类型系统允许，保留作为防御性错误。
var errNonFiniteNumeric = errors.New("unexpected NaN or Infinity in numeric column")

func decimalFromNumeric(n pgtype.Numeric) (decimal.Decimal, error) {
	if !n.Valid {
		return decimal.Decimal{}, errNullNumericForNonNullColumn
	}
	if n.NaN || n.InfinityModifier != pgtype.Finite {
		return decimal.Decimal{}, errNonFiniteNumeric
	}
	return decimal.NewFromBigInt(n.Int, n.Exp), nil
}

func nullDecimalFromNumeric(n pgtype.Numeric) (decimal.NullDecimal, error) {
	if !n.Valid {
		return decimal.NullDecimal{}, nil
	}
	d, err := decimalFromNumeric(n)
	if err != nil {
		return decimal.NullDecimal{}, err
	}
	return decimal.NullDecimal{Decimal: d, Valid: true}, nil
}

func numericFromDecimal(d decimal.Decimal) pgtype.Numeric {
	return pgtype.Numeric{
		Int:   d.Coefficient(),
		Exp:   d.Exponent(),
		Valid: true,
	}
}

func numericFromNullDecimal(d decimal.NullDecimal) pgtype.Numeric {
	if !d.Valid {
		return pgtype.Numeric{Valid: false}
	}
	return numericFromDecimal(d.Decimal)
}
