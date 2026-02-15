package asset

import (
	"time"

	"github.com/shopspring/decimal"
)

// Source 标识持仓的录入来源；取值与表 positions.source 的 CHECK 约束对齐。
type Source string

const (
	SourceManual Source = "manual" // 用户手动录入（REQ-02）
	SourceOCR    Source = "ocr"    // OCR 截图导入（V0.2）
)

// DefaultSource 是 PositionInput.Source 留空时应用的默认值。
const DefaultSource = SourceManual

// Valid 判断 Source 是否在合法集合内。
func (s Source) Valid() bool {
	return s == SourceManual || s == SourceOCR
}

// Position 是 positions 表的领域表示，字段与列一一对应。
//
// 派生字段 CostBasis、EstimatedShares 由 service 层填充，repo 仅做存取。
// EstimatedShares 在最新 T-1 净值不可用时为 NULL，由 decimal.NullDecimal 的
// Valid=false 表达，避免与零值 0 混淆（REQ-02 §3.1 / §5）。
// HoldingStartDate 仅日期分量有意义（DB 列类型 DATE），时区/时分秒由 service 在
// 计算 holding_days 时统一截断。
// Version 是乐观锁版本号：service 在 UPDATE 时把读出的值带回，repo 在 WHERE
// 中校验并自增，冲突映射为 ErrPositionVersionConflict（避免并发 PATCH 互相
// 覆盖派生字段，详见 repo.Update 注释）。
type Position struct {
	ID               int64
	FundCode         string
	HoldingAmount    decimal.Decimal
	HoldingProfit    decimal.Decimal
	CostBasis        decimal.Decimal
	EstimatedShares  decimal.NullDecimal
	HoldingDays      int
	HoldingStartDate time.Time
	Source           Source
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// PositionInput 是创建持仓的请求载荷，对应 FR-AS-01 的输入字段。
//
// 派生字段（cost_basis / estimated_shares）不在此结构内：由 service 在调用
// repo.Create 前计算并填到 Position 上，保持 repo 无业务逻辑。
//
// 可选字段语义：
//   - HoldingDays 为 nil 时取默认 0；提供时必须 >= 0
//   - HoldingStartDate 为 nil 时取当日；与 HoldingDays 同时提供时以本字段为准
//   - Source 为空时取 DefaultSource（manual）
type PositionInput struct {
	FundCode         string
	HoldingAmount    decimal.Decimal
	HoldingProfit    decimal.Decimal
	HoldingDays      *int
	HoldingStartDate *time.Time
	Source           Source
}

// PositionPatch 是修改持仓的请求载荷，对应 FR-AS-02 的可改字段子集。
//
// 字段均为指针：nil 表示"本次不修改"；非 nil 即使是零值也视为显式赋值。
// fund_code、cost_basis、estimated_shares 不可由用户直接修改，故不出现在此。
type PositionPatch struct {
	HoldingAmount    *decimal.Decimal
	HoldingProfit    *decimal.Decimal
	HoldingDays      *int
	HoldingStartDate *time.Time
}

// IsEmpty 判断是否所有字段都未设置；service 用于拒绝"空 PATCH"。
func (p PositionPatch) IsEmpty() bool {
	return p.HoldingAmount == nil &&
		p.HoldingProfit == nil &&
		p.HoldingDays == nil &&
		p.HoldingStartDate == nil
}
