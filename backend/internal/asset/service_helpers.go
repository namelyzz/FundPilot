package asset

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	apperrors "github.com/namelyzz/FundPilot/internal/platform/errors"
	"github.com/shopspring/decimal"
)

var fundCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// validateCreateInput 校验创建请求的业务约束。
func validateCreateInput(input PositionInput) error {
	switch {
	case !fundCodePattern.MatchString(input.FundCode):
		return ErrInvalidInput
	case !input.HoldingAmount.GreaterThan(decimal.Zero):
		return ErrInvalidInput
	case input.HoldingDays != nil && *input.HoldingDays < 0:
		return ErrInvalidInput
	case input.Source != "" && !input.Source.Valid():
		return ErrInvalidInput
	default:
		return nil
	}
}

// validatePatchInput 校验更新请求的业务约束。
func validatePatchInput(patch PositionPatch) error {
	switch {
	case patch.IsEmpty():
		return ErrInvalidInput
	case patch.HoldingAmount != nil && !patch.HoldingAmount.GreaterThan(decimal.Zero):
		return ErrInvalidInput
	case patch.HoldingDays != nil && *patch.HoldingDays < 0:
		return ErrInvalidInput
	default:
		return nil
	}
}

// normalizeHoldingStartDate 把日期归一化为 UTC 零点，避免时区与时分秒干扰天数计算。
func normalizeHoldingStartDate(now time.Time, input *time.Time) time.Time {
	if input != nil {
		return input.UTC().Truncate(24 * time.Hour)
	}
	return now.UTC().Truncate(24 * time.Hour)
}

// deriveHoldingDays 按 spec 口径计算最终持有天数。
func deriveHoldingDays(now, startDate time.Time, explicit *int) int {
	today := normalizeHoldingStartDate(now, nil)
	startDate = startDate.UTC().Truncate(24 * time.Hour)

	derived := int(today.Sub(startDate).Hours() / 24)
	if derived < 0 {
		derived = 0
	}
	if explicit != nil && *explicit > derived {
		return *explicit
	}
	return derived
}

// explicitHoldingDaysForUpdate 决定 Update 流程中 holding_days 的"显式输入"来源。
//
// 规则：
//   - 若本次 PATCH 显式提供了 HoldingDays，则尊重该值参与 max(显式值, 推导值)
//   - 若只改了 HoldingStartDate，则视为用户希望按新的起始日期重新推导，不沿用旧 stored 值
//   - 若两者都没改，则沿用存量 holding_days 参与 max，以保持既有口径稳定
func explicitHoldingDaysForUpdate(patch PositionPatch, current int) *int {
	if patch.HoldingDays != nil {
		return patch.HoldingDays
	}
	if patch.HoldingStartDate != nil {
		return nil
	}
	return &current
}

// deriveCostBasis 反推持仓本金。
func deriveCostBasis(holdingAmount, holdingProfit decimal.Decimal) decimal.Decimal {
	return holdingAmount.Sub(holdingProfit)
}

// historyRange 把 rangeSpec 转为查询区间；仅接受 1d / 7d，端点为闭区间。
func historyRange(now time.Time, rangeSpec string) (time.Time, time.Time, error) {
	switch rangeSpec {
	case "1d":
		return now.Add(-24 * time.Hour), now, nil
	case "7d":
		return now.Add(-7 * 24 * time.Hour), now, nil
	default:
		return time.Time{}, time.Time{}, ErrInvalidInput
	}
}

// collectFundCodes 提取去重后的 fund_code，保持首次出现顺序。
func collectFundCodes(positions []Position) []string {
	seen := make(map[string]struct{}, len(positions))
	out := make([]string, 0, len(positions))
	for _, pos := range positions {
		if _, ok := seen[pos.FundCode]; ok {
			continue
		}
		seen[pos.FundCode] = struct{}{}
		out = append(out, pos.FundCode)
	}
	return out
}

// collectPositionIDs 提取持仓 ID，保持 repo.List 的返回顺序。
func collectPositionIDs(positions []Position) []int64 {
	out := make([]int64, 0, len(positions))
	for _, pos := range positions {
		out = append(out, pos.ID)
	}
	return out
}

// confidenceSummarySeed 返回稳定的置信度汇总初值，确保输出字段始终完整。
func confidenceSummarySeed() map[string]int {
	return map[string]int{
		"high":        0,
		"mid":         0,
		"low":         0,
		"unsupported": 0,
	}
}

// immediateTodayReturnPct 计算即时聚合口径的当日收益率，避免除零。
func immediateTodayReturnPct(totalAssets, todayPnL decimal.Decimal) decimal.Decimal {
	if !totalAssets.GreaterThan(decimal.Zero) {
		return decimal.Zero
	}
	denominator := totalAssets.Sub(todayPnL)
	if denominator.Equal(decimal.Zero) {
		return decimal.Zero
	}
	return todayPnL.Div(denominator)
}

// wrapUnexpected 把非业务错误统一包装为 service 层内部错误。
func wrapUnexpected(err error) error {
	if err == nil {
		return nil
	}
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		return err
	}
	return fmt.Errorf("asset service: %v: %w", err, ErrInternalServer)
}
