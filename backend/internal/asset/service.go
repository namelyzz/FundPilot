package asset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/namelyzz/FundPilot/internal/platform/logger"
	"github.com/shopspring/decimal"
)

// Service 是 asset 域的应用服务层，负责编排 repo 与跨域查询接口完成各 use case。
type Service struct {
	repo            Repository      // 持仓持久化，asset 域自有 CRUD
	fundLookup      FundLookup      // 基金元数据 / T-1 净值查询，由 REQ-03 实现；不可用时降级
	valuationLookup ValuationLookup // 估值快照查询，由 REQ-05 实现；不可用时降级
}

// NewService 构造 asset.Service；依赖由装配层注入。
func NewService(repo Repository, fundLookup FundLookup, valuationLookup ValuationLookup) *Service {
	return &Service{
		repo:            repo,
		fundLookup:      fundLookup,
		valuationLookup: valuationLookup,
	}
}

// CreatePosition 实现 FR-AS-01：新建持仓。
func (s *Service) CreatePosition(ctx context.Context, input PositionInput) (*Position, error) {
	if err := validateCreateInput(input); err != nil {
		return nil, err
	}

	now := time.Now()
	source := input.Source
	if source == "" {
		source = DefaultSource
	}

	startDate := normalizeHoldingStartDate(now, input.HoldingStartDate)
	pos := &Position{
		FundCode:         input.FundCode,
		HoldingAmount:    input.HoldingAmount,
		HoldingProfit:    input.HoldingProfit,
		CostBasis:        deriveCostBasis(input.HoldingAmount, input.HoldingProfit),
		HoldingDays:      deriveHoldingDays(now, startDate, input.HoldingDays),
		HoldingStartDate: startDate,
		Source:           source,
	}

	shares, err := s.resolveShares(ctx, input.FundCode, input.HoldingAmount, 0)
	if err != nil {
		return nil, err
	}
	pos.EstimatedShares = shares

	created, err := s.repo.Create(ctx, pos)
	if err != nil {
		switch {
		case errors.Is(err, ErrPositionDuplicate):
			return nil, err
		default:
			return nil, wrapUnexpected(err)
		}
	}
	return created, nil
}

// UpdatePosition 实现 FR-AS-02：修改持仓。
func (s *Service) UpdatePosition(ctx context.Context, id int64, patch PositionPatch) (*Position, error) {
	if err := validatePatchInput(patch); err != nil {
		return nil, err
	}

	pos, err := s.repo.GetByID(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, ErrPositionNotFound):
			return nil, err
		default:
			return nil, wrapUnexpected(err)
		}
	}

	explicitHoldingDays := explicitHoldingDaysForUpdate(patch, pos.HoldingDays)
	now := time.Now()

	if patch.HoldingAmount != nil {
		pos.HoldingAmount = *patch.HoldingAmount
	}
	if patch.HoldingProfit != nil {
		pos.HoldingProfit = *patch.HoldingProfit
	}
	if patch.HoldingDays != nil {
		pos.HoldingDays = *patch.HoldingDays
	}
	if patch.HoldingStartDate != nil {
		pos.HoldingStartDate = normalizeHoldingStartDate(now, patch.HoldingStartDate)
	}

	pos.CostBasis = deriveCostBasis(pos.HoldingAmount, pos.HoldingProfit)
	pos.HoldingDays = deriveHoldingDays(now, pos.HoldingStartDate, explicitHoldingDays)

	if patch.HoldingAmount != nil {
		shares, err := s.resolveShares(ctx, pos.FundCode, pos.HoldingAmount, pos.ID)
		if err != nil {
			return nil, err
		}
		pos.EstimatedShares = shares
	}

	updated, err := s.repo.Update(ctx, pos)
	if err != nil {
		switch {
		case errors.Is(err, ErrPositionNotFound),
			errors.Is(err, ErrPositionVersionConflict):
			return nil, err
		default:
			return nil, wrapUnexpected(err)
		}
	}
	return updated, nil
}

// DeletePosition 实现 FR-AS-03：删除持仓。
func (s *Service) DeletePosition(ctx context.Context, id int64) error {
	err := s.repo.Delete(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, ErrPositionNotFound):
			return err
		default:
			return wrapUnexpected(err)
		}
	}
	return nil
}

// ListPositions 实现 FR-AS-04：查询持仓列表及其衍生信息。
// fund / valuation 查询整体失败时降级：对应字段全部为 nil，不阻断列表返回。
func (s *Service) ListPositions(ctx context.Context) ([]PositionListItem, error) {
	positions, err := s.repo.List(ctx)
	if err != nil {
		return nil, wrapUnexpected(err)
	}
	if len(positions) == 0 {
		return []PositionListItem{}, nil
	}

	fundMetas := map[string]FundMeta{}
	fundCodes := collectFundCodes(positions)
	if len(fundCodes) > 0 {
		fundMetas, err = s.fundLookup.MetaBatch(ctx, fundCodes)
		if err != nil {
			logger.FromContext(ctx).Warn(
				"asset.list_positions: fund meta lookup failed; degrade fund fields to null",
				"fund_codes", fundCodes,
				"err", err.Error(),
			)
			fundMetas = map[string]FundMeta{}
		}
	}

	valuations := map[int64]PositionValuation{}
	positionIDs := collectPositionIDs(positions)
	if len(positionIDs) > 0 {
		valuations, err = s.valuationLookup.LatestBatch(ctx, positionIDs)
		if err != nil {
			logger.FromContext(ctx).Warn(
				"asset.list_positions: latest valuation lookup failed; degrade valuation fields to null",
				"position_ids", positionIDs,
				"err", err.Error(),
			)
			valuations = map[int64]PositionValuation{}
		}
	}

	now := time.Now()
	items := make([]PositionListItem, 0, len(positions))
	for _, pos := range positions {
		items = append(items, toPositionListItem(pos, fundMetas, valuations, now))
	}
	return items, nil
}

// GetPositionHistory 实现 FR-AS-05：查询单条持仓的估值历史。
// valuation 域不可用时降级为空切片，不阻断返回。
func (s *Service) GetPositionHistory(ctx context.Context, id int64, rangeSpec string) ([]PositionValuation, error) {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		switch {
		case errors.Is(err, ErrPositionNotFound):
			return nil, err
		default:
			return nil, wrapUnexpected(err)
		}
	}

	from, to, err := historyRange(time.Now(), rangeSpec)
	if err != nil {
		return nil, err
	}

	history, err := s.valuationLookup.Range(ctx, id, from, to)
	if err != nil {
		logger.FromContext(ctx).Warn(
			"asset.get_position_history: valuation range lookup failed; degrade to empty history",
			"position_id", id,
			"range_spec", rangeSpec,
			"err", err.Error(),
		)
		return []PositionValuation{}, nil
	}
	if history == nil {
		return []PositionValuation{}, nil
	}
	return history, nil
}

// GetPortfolioOverview 实现 FR-AS-06：查询组合总览。
func (s *Service) GetPortfolioOverview(ctx context.Context) (*PortfolioOverview, error) {
	snapshot, err := s.valuationLookup.PortfolioOverview(ctx)
	switch {
	case err == nil:
		return toPortfolioOverview(snapshot), nil
	case errors.Is(err, ErrValuationNotFound):
		logger.FromContext(ctx).Warn(
			"asset.get_portfolio_overview: snapshot not found; fallback to immediate calculation",
		)
		return s.immediatePortfolioOverview(ctx)
	default:
		return nil, wrapUnexpected(err)
	}
}

// resolveShares 查询基金份额，处理 FundNotFound 降级，其它错误上抛。
// posID 在新建时传 0，更新时传实际 ID，仅用于日志追踪。
func (s *Service) resolveShares(ctx context.Context, fundCode string, amount decimal.Decimal, posID int64) (decimal.NullDecimal, error) {
	shares, err := s.deriveEstimatedShares(ctx, fundCode, amount)
	switch {
	case err == nil:
		return shares, nil
	case errors.Is(err, ErrFundNotFound):
		logger.FromContext(ctx).Warn(
			"asset.resolve_shares: fund nav not found, degrade to null",
			"fund_code", fundCode,
			"position_id", posID,
		)
		return decimal.NullDecimal{}, nil
	default:
		logger.FromContext(ctx).Error(
			"asset.resolve_shares: fund nav lookup failed",
			"fund_code", fundCode,
			"position_id", posID,
			"err", err,
		)
		return decimal.NullDecimal{}, wrapUnexpected(err)
	}
}

// immediatePortfolioOverview 在快照不存在时按最新估值即时聚合组合总览。
func (s *Service) immediatePortfolioOverview(ctx context.Context) (*PortfolioOverview, error) {
	positions, err := s.repo.List(ctx)
	if err != nil {
		return nil, wrapUnexpected(err)
	}

	valuations := map[int64]PositionValuation{}
	positionIDs := collectPositionIDs(positions)
	if len(positionIDs) > 0 {
		valuations, err = s.valuationLookup.LatestBatch(ctx, positionIDs)
		if err != nil {
			logger.FromContext(ctx).Warn(
				"asset.get_portfolio_overview: latest valuations unavailable during fallback; degrade to empty valuations",
				"position_ids", positionIDs,
				"err", err.Error(),
			)
			valuations = map[int64]PositionValuation{}
		}
	}

	totalAssets := decimal.Zero
	todayPnL := decimal.Zero
	confidenceSummary := confidenceSummarySeed()
	for _, pos := range positions {
		valuation, ok := valuations[pos.ID]
		if !ok {
			continue
		}
		if valuation.EstMarketValue.Valid {
			totalAssets = totalAssets.Add(valuation.EstMarketValue.Decimal)
		}
		if valuation.TodayPnL.Valid {
			todayPnL = todayPnL.Add(valuation.TodayPnL.Decimal)
		}
		if valuation.Confidence == "" {
			continue
		}
		confidenceSummary[valuation.Confidence]++
	}

	return &PortfolioOverview{
		TotalAssets:       totalAssets,
		TodayPnL:          todayPnL,
		TodayReturnPct:    immediateTodayReturnPct(totalAssets, todayPnL),
		PositionCount:     len(positions),
		ConfidenceSummary: confidenceSummary,
		AsOf:              time.Now(),
	}, nil
}

// deriveEstimatedShares 通过最新净值反推份额。
func (s *Service) deriveEstimatedShares(ctx context.Context, fundCode string, holdingAmount decimal.Decimal) (decimal.NullDecimal, error) {
	nav, err := s.fundLookup.LatestNAV(ctx, fundCode)
	if err != nil {
		return decimal.NullDecimal{}, err
	}
	if !nav.Value.GreaterThan(decimal.Zero) {
		return decimal.NullDecimal{}, fmt.Errorf("asset: latest nav for %s must be positive", fundCode)
	}
	return decimal.NullDecimal{
		Decimal: holdingAmount.Div(nav.Value),
		Valid:   true,
	}, nil
}
