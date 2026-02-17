package asset

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// 本文件定义 asset 域消费其它领域所需的契约接口(consumer defines the interface)
//   - FundLookup     —— 由 fund 域实现
//   - ValuationLookup —— 由 valuation 域实现
//
// REQ-02 阶段（B / C / D）这些接口在 cmd/fundpilot/stubs.go 提供 null 桩，
// MetaBatch / LatestBatch 返回空 map，单条查询返回对应 sentinel
// （ErrFundNotFound / ErrValuationNotFound），service 在收到 sentinel 时降级为
// "fund 字段为 null" / "估值字段为 null"（spec §9 验收清单）。
//
// DTO 命名约定：跨域 DTO 一律放本文件，避免 service 反向 import fund / valuation
// 包。owner 域内部可有更丰富的实体类型；这里只暴露 asset 真正消费的字段子集。

// ---- Fund 域接口 ---------------------------------------------------------

// FundLookup 暴露 asset 域所需的 fund 元数据 / 净值查询能力。
type FundLookup interface {
	// MetaBatch 批量拉取多只基金的元数据；找不到的 fund_code 不进 map
	// （不视为错误），由调用方按"缺失即 null"处理。
	// 用于 FR-AS-04 列表的 fund_name / fund_type 字段拼装，避免 N+1。
	MetaBatch(ctx context.Context, fundCodes []string) (map[string]FundMeta, error)

	// LatestNAV 拉取单只基金的最新 T-1 净值。
	// 找不到时返回 [ErrFundNotFound]，调用方降级（如 estimated_shares 置 NULL）。
	// 用于 FR-AS-01 / FR-AS-02 创建或修改持仓时反推 estimated_shares。
	LatestNAV(ctx context.Context, fundCode string) (NAV, error)
}

// FundMeta 是 asset 域消费的基金元数据快照。
type FundMeta struct {
	Code string // 基金代码（对齐 positions.fund_code）
	Name string // 基金全称
	Type string // 基金类型：index | etf_link | stock | bond | mixed | qdii | other
}

// NAV 是基金的 T-1 净值快照。FetchedAt 用于诊断"净值多久没刷新过"；
// 业务计算（estimated_shares）只用 Value。
type NAV struct {
	FundCode  string          // 基金代码
	Value     decimal.Decimal // T-1 单位净值
	NAVDate   time.Time       // T-1 净值对应的交易日（DATE 列，时分秒可忽略）
	FetchedAt time.Time       // 上游最近一次同步成功的时间
}

// ---- Valuation 域接口 ----------------------------------------------------

// ValuationLookup 暴露 asset 域所需的估值快照查询能力。
type ValuationLookup interface {
	// LatestBatch 批量拉取多条持仓的最新估值快照；找不到的 positionID 不进 map。
	// 用于 FR-AS-04 列表的 est_* 字段拼装，避免 N+1。
	LatestBatch(ctx context.Context, positionIDs []int64) (map[int64]PositionValuation, error)

	// Range 拉取单条持仓在 [from, to] 区间内的估值序列，按 as_of 升序。
	// 用于 FR-AS-05 GET /api/positions/{id}/history；
	// 区间内无数据时返回空切片（不视为错误）。
	Range(ctx context.Context, positionID int64, from, to time.Time) ([]PositionValuation, error)

	// PortfolioOverview 返回最新一次组合级快照。
	// 当尚未生成任何快照时返回 [ErrValuationNotFound]，
	// 调用方按 FR-AS-06 改走"即时计算一次"路径。
	PortfolioOverview(ctx context.Context) (PortfolioSnapshot, error)
}

// PositionValuation 是单条持仓的估值时序快照（对齐表 position_valuations）。
// 多个数值字段允许 NULL（spec §3.2 / 迁移 0002）：未填充时 Valid=false。
// Confidence 用 string 而非自定义类型——枚举所有权属于 valuation 域，
// asset 仅消费透传，避免跨包枚举漂移。
type PositionValuation struct {
	PositionID     int64               // 持仓 ID，逻辑外键 → positions.id
	AsOf           time.Time           // 估值时间点（hypertable 分区键）
	EstNAV         decimal.NullDecimal // 估算净值，缺省时 Valid=false
	EstChangePct   decimal.NullDecimal // 估算涨跌幅，缺省时 Valid=false
	EstMarketValue decimal.NullDecimal // 估算市值，缺省时 Valid=false
	TodayPnL       decimal.NullDecimal // 当日估算盈亏，缺省时 Valid=false
	Confidence     string              // high | mid | low | unsupported
	CoverageRatio  decimal.NullDecimal // 估值覆盖度 0.0000–1.0000
	FallbackReason string              // 降级原因；空串 = 走主路径
}

// PortfolioSnapshot 是组合级估值快照（对齐表 portfolio_snapshots）。
// ConfidenceSummary 用 map 表达 JSONB 字段的稳定结构（spec §3.3 / FR-AS-06）。
type PortfolioSnapshot struct {
	AsOf              time.Time       // 快照时间点
	TotalAssets       decimal.Decimal // 所有持仓 EstMarketValue 之和
	TodayPnL          decimal.Decimal // 组合当日估算盈亏
	TodayReturnPct    decimal.Decimal // 组合当日收益率
	PositionCount     int             // 持仓数量
	ConfidenceSummary map[string]int  // {"high":N,"mid":N,"low":N,"unsupported":N}
}
