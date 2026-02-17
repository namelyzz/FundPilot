package asset

import (
	"time"

	"github.com/shopspring/decimal"
)

// PositionListItem 是 FR-AS-04 列表接口的单条输出。
type PositionListItem struct {
	ID                int64            `json:"id"`                  // 持仓 ID
	FundCode          string           `json:"fund_code"`           // 基金代码
	HoldingAmount     decimal.Decimal  `json:"holding_amount"`      // 当前市值
	HoldingProfit     decimal.Decimal  `json:"holding_profit"`      // 累计盈亏
	HoldingProfitRate decimal.Decimal  `json:"holding_profit_rate"` // 收益率 = HoldingProfit / CostBasis；CostBasis 为 0 时返回 0
	HoldingDays       int              `json:"holding_days"`        // 持有天数，已取 max(stored, today - startDate)
	FundName          *string          `json:"fund_name"`           // 基金全称，fund 域不可用时为 nil
	FundType          *string          `json:"fund_type"`           // 基金类型，fund 域不可用时为 nil
	EstNAV            *decimal.Decimal `json:"est_nav"`             // 估算净值（T+1 估值），valuation 域不可用时为 nil
	EstChangePct      *decimal.Decimal `json:"est_change_pct"`      // 估算涨跌幅，valuation 域不可用时为 nil
	EstMarketValue    *decimal.Decimal `json:"est_market_value"`    // 估算市值，valuation 域不可用时为 nil
	TodayPnL          *decimal.Decimal `json:"today_pnl"`           // 当日估算盈亏，valuation 域不可用时为 nil
	Confidence        *string          `json:"confidence"`          // 估值置信度：high / mid / low / unsupported，缺省时为 nil
	ValuationAsOf     *time.Time       `json:"valuation_as_of"`     // 估值快照时间点，缺省时为 nil
}

// PortfolioOverview 是 FR-AS-06 的输出。
type PortfolioOverview struct {
	TotalAssets       decimal.Decimal `json:"total_assets"`       // 组合总市值，所有持仓 EstMarketValue 之和
	TodayPnL          decimal.Decimal `json:"today_pnl"`          // 组合当日估算盈亏，所有持仓 TodayPnL 之和
	TodayReturnPct    decimal.Decimal `json:"today_return_pct"`   // 组合当日收益率 = TodayPnL / (TotalAssets - TodayPnL)
	PositionCount     int             `json:"position_count"`     // 持仓数量
	ConfidenceSummary map[string]int  `json:"confidence_summary"` // 估值置信度分布 {"high":N,"mid":N,"low":N,"unsupported":N}
	AsOf              time.Time       `json:"as_of"`              // 快照时间；即时计算路径下为 time.Now()
}

// toPositionListItem 把 Position 与跨域数据组装为 FR-AS-04 输出 DTO。
func toPositionListItem(pos Position, fundMetas map[string]FundMeta, valuations map[int64]PositionValuation, now time.Time) PositionListItem {
	item := PositionListItem{
		ID:                pos.ID,
		FundCode:          pos.FundCode,
		HoldingAmount:     pos.HoldingAmount,
		HoldingProfit:     pos.HoldingProfit,
		HoldingProfitRate: profitRate(pos.HoldingProfit, pos.CostBasis),
		HoldingDays:       deriveHoldingDays(now, pos.HoldingStartDate, &pos.HoldingDays),
	}

	if meta, ok := fundMetas[pos.FundCode]; ok {
		item.FundName = stringPtr(meta.Name)
		item.FundType = stringPtr(meta.Type)
	}

	if valuation, ok := valuations[pos.ID]; ok {
		item.EstNAV = decimalPtr(valuation.EstNAV)
		item.EstChangePct = decimalPtr(valuation.EstChangePct)
		item.EstMarketValue = decimalPtr(valuation.EstMarketValue)
		item.TodayPnL = decimalPtr(valuation.TodayPnL)
		item.Confidence = stringPtr(valuation.Confidence)
		item.ValuationAsOf = timePtr(valuation.AsOf)
	}

	return item
}

// toPortfolioOverview 把组合级快照转换为 FR-AS-06 输出 DTO。
func toPortfolioOverview(snapshot PortfolioSnapshot) *PortfolioOverview {
	return &PortfolioOverview{
		TotalAssets:       snapshot.TotalAssets,
		TodayPnL:          snapshot.TodayPnL,
		TodayReturnPct:    snapshot.TodayReturnPct,
		PositionCount:     snapshot.PositionCount,
		ConfidenceSummary: cloneConfidenceSummary(snapshot.ConfidenceSummary),
		AsOf:              snapshot.AsOf,
	}
}

// profitRate 计算累计收益率；本金为 0 时返回 0，避免除零。
func profitRate(holdingProfit, costBasis decimal.Decimal) decimal.Decimal {
	if costBasis.Equal(decimal.Zero) {
		return decimal.Zero
	}
	return holdingProfit.Div(costBasis)
}

func decimalPtr(v decimal.NullDecimal) *decimal.Decimal {
	if !v.Valid {
		return nil
	}
	d := v.Decimal
	return &d
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	s := v
	return &s
}

func timePtr(v time.Time) *time.Time {
	if v.IsZero() {
		return nil
	}
	t := v
	return &t
}

func cloneConfidenceSummary(in map[string]int) map[string]int {
	out := confidenceSummarySeed()
	for key, value := range in {
		out[key] = value
	}
	return out
}
