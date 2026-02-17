package asset

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

type mockRepository struct {
	positions map[int64]*Position
	nextID    int64

	createErr  error
	getByIDErr error
	updateErr  error
	deleteErr  error
}

func newMockRepository() *mockRepository {
	return &mockRepository{
		positions: make(map[int64]*Position),
		nextID:    1,
	}
}

func (m *mockRepository) Create(_ context.Context, p *Position) (*Position, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	for _, existing := range m.positions {
		if existing.FundCode == p.FundCode {
			return nil, ErrPositionDuplicate
		}
	}

	now := time.Now().UTC()
	created := clonePosition(p)
	created.ID = m.nextID
	created.Version = 0
	created.CreatedAt = now
	created.UpdatedAt = now

	m.positions[created.ID] = created
	m.nextID++
	return clonePosition(created), nil
}

func (m *mockRepository) GetByID(_ context.Context, id int64) (*Position, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	pos, ok := m.positions[id]
	if !ok {
		return nil, ErrPositionNotFound
	}
	return clonePosition(pos), nil
}

func (m *mockRepository) GetByFundCode(_ context.Context, fundCode string) (*Position, error) {
	for _, pos := range m.positions {
		if pos.FundCode == fundCode {
			return clonePosition(pos), nil
		}
	}
	return nil, ErrPositionNotFound
}

func (m *mockRepository) Update(_ context.Context, p *Position) (*Position, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}

	existing, ok := m.positions[p.ID]
	if !ok {
		return nil, ErrPositionNotFound
	}
	if existing.Version != p.Version {
		return nil, ErrPositionVersionConflict
	}

	updated := clonePosition(p)
	updated.Version = existing.Version + 1
	updated.CreatedAt = existing.CreatedAt
	updated.UpdatedAt = time.Now().UTC()

	m.positions[p.ID] = updated
	return clonePosition(updated), nil
}

func (m *mockRepository) Delete(_ context.Context, id int64) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.positions[id]; !ok {
		return ErrPositionNotFound
	}
	delete(m.positions, id)
	return nil
}

func (m *mockRepository) List(_ context.Context) ([]Position, error) {
	out := make([]Position, 0, len(m.positions))
	for _, pos := range m.positions {
		out = append(out, *clonePosition(pos))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

type mockFundLookup struct {
	metas     map[string]FundMeta
	navs      map[string]NAV
	metaErr   error
	errs      map[string]error
	callCount int
}

func newMockFundLookup() *mockFundLookup {
	return &mockFundLookup{
		metas: make(map[string]FundMeta),
		navs:  make(map[string]NAV),
		errs:  make(map[string]error),
	}
}

func (m *mockFundLookup) MetaBatch(_ context.Context, fundCodes []string) (map[string]FundMeta, error) {
	if m.metaErr != nil {
		return nil, m.metaErr
	}
	out := make(map[string]FundMeta, len(fundCodes))
	for _, code := range fundCodes {
		if meta, ok := m.metas[code]; ok {
			out[code] = meta
		}
	}
	return out, nil
}

func (m *mockFundLookup) LatestNAV(_ context.Context, fundCode string) (NAV, error) {
	m.callCount++
	if err, ok := m.errs[fundCode]; ok {
		return NAV{}, err
	}
	nav, ok := m.navs[fundCode]
	if !ok {
		return NAV{}, ErrFundNotFound
	}
	return nav, nil
}

type mockValuationLookup struct {
	latest       map[int64]PositionValuation
	latestErr    error
	ranges       map[int64][]PositionValuation
	rangeErr     error
	portfolio    *PortfolioSnapshot
	portfolioErr error
}

func (m *mockValuationLookup) LatestBatch(_ context.Context, positionIDs []int64) (map[int64]PositionValuation, error) {
	if m.latestErr != nil {
		return nil, m.latestErr
	}
	out := make(map[int64]PositionValuation, len(positionIDs))
	for _, id := range positionIDs {
		if valuation, ok := m.latest[id]; ok {
			out[id] = valuation
		}
	}
	return out, nil
}

func (m *mockValuationLookup) Range(_ context.Context, positionID int64, from, to time.Time) ([]PositionValuation, error) {
	_ = positionID
	_ = from
	_ = to
	if m.rangeErr != nil {
		return nil, m.rangeErr
	}
	if m.ranges == nil {
		return []PositionValuation{}, nil
	}
	return m.ranges[positionID], nil
}

func (m *mockValuationLookup) PortfolioOverview(_ context.Context) (PortfolioSnapshot, error) {
	if m.portfolioErr != nil {
		return PortfolioSnapshot{}, m.portfolioErr
	}
	if m.portfolio != nil {
		return *m.portfolio, nil
	}
	return PortfolioSnapshot{}, ErrValuationNotFound
}

func clonePosition(in *Position) *Position {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func newTestService(repo *mockRepository, funds *mockFundLookup) *Service {
	return NewService(repo, funds, &mockValuationLookup{
		latest: make(map[int64]PositionValuation),
		ranges: make(map[int64][]PositionValuation),
	})
}

func newTestServiceWithValuations(repo *mockRepository, funds *mockFundLookup, valuations *mockValuationLookup) *Service {
	return NewService(repo, funds, valuations)
}

func TestService_CreatePosition_Success(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	funds.navs["000001"] = NAV{FundCode: "000001", Value: mustDecimal(t, "2.00")}

	svc := newTestService(repo, funds)
	days := 7
	input := PositionInput{
		FundCode:      "000001",
		HoldingAmount: mustDecimal(t, "100.00"),
		HoldingProfit: mustDecimal(t, "12.50"),
		HoldingDays:   &days,
	}

	got, err := svc.CreatePosition(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePosition: %v", err)
	}

	if got.ID == 0 {
		t.Fatal("CreatePosition: ID not assigned")
	}
	if got.Source != DefaultSource {
		t.Fatalf("Source = %q, want %q", got.Source, DefaultSource)
	}
	if !got.CostBasis.Equal(mustDecimal(t, "87.50")) {
		t.Fatalf("CostBasis = %s, want 87.50", got.CostBasis)
	}
	if got.HoldingDays != 7 {
		t.Fatalf("HoldingDays = %d, want 7", got.HoldingDays)
	}
	if !got.EstimatedShares.Valid {
		t.Fatal("EstimatedShares.Valid = false, want true")
	}
	if !got.EstimatedShares.Decimal.Equal(mustDecimal(t, "50.00")) {
		t.Fatalf("EstimatedShares = %s, want 50.00", got.EstimatedShares.Decimal)
	}
}

func TestService_CreatePosition_Duplicate(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	funds.navs["000002"] = NAV{FundCode: "000002", Value: mustDecimal(t, "1.00")}
	svc := newTestService(repo, funds)

	input := PositionInput{
		FundCode:      "000002",
		HoldingAmount: mustDecimal(t, "100.00"),
		HoldingProfit: mustDecimal(t, "0"),
	}

	if _, err := svc.CreatePosition(context.Background(), input); err != nil {
		t.Fatalf("first CreatePosition: %v", err)
	}
	_, err := svc.CreatePosition(context.Background(), input)
	if !errors.Is(err, ErrPositionDuplicate) {
		t.Fatalf("second CreatePosition: want ErrPositionDuplicate, got %v", err)
	}
}

func TestService_CreatePosition_InvalidInput(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	_, err := svc.CreatePosition(context.Background(), PositionInput{
		FundCode:      "bad",
		HoldingAmount: mustDecimal(t, "100.00"),
		HoldingProfit: decimal.Zero,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreatePosition: want ErrInvalidInput, got %v", err)
	}
}

func TestService_CreatePosition_FundNotAvailable_Degraded(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	funds.errs["000003"] = ErrFundNotFound
	svc := newTestService(repo, funds)

	got, err := svc.CreatePosition(context.Background(), PositionInput{
		FundCode:      "000003",
		HoldingAmount: mustDecimal(t, "100.00"),
		HoldingProfit: decimal.Zero,
	})
	if err != nil {
		t.Fatalf("CreatePosition: %v", err)
	}
	if got.EstimatedShares.Valid {
		t.Fatalf("EstimatedShares.Valid = true, want false")
	}
}

func TestService_CreatePosition_FundLookupUnexpectedError(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	funds.errs["000008"] = errors.New("upstream timeout")
	svc := newTestService(repo, funds)

	got, err := svc.CreatePosition(context.Background(), PositionInput{
		FundCode:      "000008",
		HoldingAmount: mustDecimal(t, "100.00"),
		HoldingProfit: decimal.Zero,
	})
	if got != nil {
		t.Fatalf("CreatePosition returned position on error: %+v", got)
	}
	if !errors.Is(err, ErrInternalServer) {
		t.Fatalf("CreatePosition: want ErrInternalServer, got %v", err)
	}
}

func TestService_UpdatePosition_Success_RecomputesDerivedFields(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	funds.navs["000004"] = NAV{FundCode: "000004", Value: mustDecimal(t, "2.50")}
	svc := newTestService(repo, funds)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000004",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    mustDecimal(t, "10.00"),
		CostBasis:        mustDecimal(t, "90.00"),
		EstimatedShares:  decimal.NullDecimal{Decimal: mustDecimal(t, "40.00"), Valid: true},
		HoldingDays:      30,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	amount := mustDecimal(t, "125.00")
	profit := mustDecimal(t, "5.00")
	got, err := svc.UpdatePosition(context.Background(), created.ID, PositionPatch{
		HoldingAmount: &amount,
		HoldingProfit: &profit,
	})
	if err != nil {
		t.Fatalf("UpdatePosition: %v", err)
	}

	if !got.CostBasis.Equal(mustDecimal(t, "120.00")) {
		t.Fatalf("CostBasis = %s, want 120.00", got.CostBasis)
	}
	if got.HoldingDays != 30 {
		t.Fatalf("HoldingDays = %d, want 30", got.HoldingDays)
	}
	if !got.EstimatedShares.Valid {
		t.Fatal("EstimatedShares.Valid = false, want true")
	}
	if !got.EstimatedShares.Decimal.Equal(mustDecimal(t, "50.00")) {
		t.Fatalf("EstimatedShares = %s, want 50.00", got.EstimatedShares.Decimal)
	}
	if got.Version != 1 {
		t.Fatalf("Version = %d, want 1", got.Version)
	}
}

func TestService_UpdatePosition_NotFound(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	amount := mustDecimal(t, "120.00")
	_, err := svc.UpdatePosition(context.Background(), 999, PositionPatch{
		HoldingAmount: &amount,
	})
	if !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("UpdatePosition: want ErrPositionNotFound, got %v", err)
	}
}

func TestService_UpdatePosition_EmptyPatch(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	_, err := svc.UpdatePosition(context.Background(), 1, PositionPatch{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdatePosition: want ErrInvalidInput, got %v", err)
	}
}

func TestService_UpdatePosition_VersionConflict(t *testing.T) {
	repo := newMockRepository()
	repo.updateErr = ErrPositionVersionConflict
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000005",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    decimal.Zero,
		CostBasis:        mustDecimal(t, "100.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	amount := mustDecimal(t, "110.00")
	_, err = svc.UpdatePosition(context.Background(), created.ID, PositionPatch{
		HoldingAmount: &amount,
	})
	if !errors.Is(err, ErrPositionVersionConflict) {
		t.Fatalf("UpdatePosition: want ErrPositionVersionConflict, got %v", err)
	}
}

func TestService_UpdatePosition_WithoutAmount_KeepsEstimatedShares(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000006",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    mustDecimal(t, "10.00"),
		CostBasis:        mustDecimal(t, "90.00"),
		EstimatedShares:  decimal.NullDecimal{Decimal: mustDecimal(t, "45.00"), Valid: true},
		HoldingDays:      3,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	profit := mustDecimal(t, "12.00")
	got, err := svc.UpdatePosition(context.Background(), created.ID, PositionPatch{
		HoldingProfit: &profit,
	})
	if err != nil {
		t.Fatalf("UpdatePosition: %v", err)
	}
	if funds.callCount != 0 {
		t.Fatalf("LatestNAV calls = %d, want 0", funds.callCount)
	}
	if !got.EstimatedShares.Valid || !got.EstimatedShares.Decimal.Equal(mustDecimal(t, "45.00")) {
		t.Fatalf("EstimatedShares = %+v, want unchanged 45.00", got.EstimatedShares)
	}
}

func TestService_UpdatePosition_StartDateWithoutHoldingDays_RecomputesFromStartDate(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	oldStart := time.Now().UTC().Truncate(24 * time.Hour).Add(-30 * 24 * time.Hour)
	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000009",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    mustDecimal(t, "10.00"),
		CostBasis:        mustDecimal(t, "90.00"),
		EstimatedShares:  decimal.NullDecimal{Decimal: mustDecimal(t, "45.00"), Valid: true},
		HoldingDays:      30,
		HoldingStartDate: oldStart,
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	newStart := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	got, err := svc.UpdatePosition(context.Background(), created.ID, PositionPatch{
		HoldingStartDate: &newStart,
	})
	if err != nil {
		t.Fatalf("UpdatePosition: %v", err)
	}
	if got.HoldingDays != 1 {
		t.Fatalf("HoldingDays = %d, want 1", got.HoldingDays)
	}
}

func TestService_UpdatePosition_FundLookupUnexpectedError(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	funds.errs["000010"] = errors.New("upstream timeout")
	svc := newTestService(repo, funds)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000010",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    mustDecimal(t, "10.00"),
		CostBasis:        mustDecimal(t, "90.00"),
		EstimatedShares:  decimal.NullDecimal{Decimal: mustDecimal(t, "45.00"), Valid: true},
		HoldingDays:      3,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	amount := mustDecimal(t, "120.00")
	got, err := svc.UpdatePosition(context.Background(), created.ID, PositionPatch{
		HoldingAmount: &amount,
	})
	if got != nil {
		t.Fatalf("UpdatePosition returned position on error: %+v", got)
	}
	if !errors.Is(err, ErrInternalServer) {
		t.Fatalf("UpdatePosition: want ErrInternalServer, got %v", err)
	}

	stored, err := repo.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("repo.GetByID: %v", err)
	}
	if !stored.HoldingAmount.Equal(mustDecimal(t, "100.00")) {
		t.Fatalf("stored HoldingAmount = %s, want unchanged 100.00", stored.HoldingAmount)
	}
	if !stored.EstimatedShares.Valid || !stored.EstimatedShares.Decimal.Equal(mustDecimal(t, "45.00")) {
		t.Fatalf("stored EstimatedShares = %+v, want unchanged 45.00", stored.EstimatedShares)
	}
}

func TestService_DeletePosition_Success(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000007",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    decimal.Zero,
		CostBasis:        mustDecimal(t, "100.00"),
		HoldingDays:      0,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	if err := svc.DeletePosition(context.Background(), created.ID); err != nil {
		t.Fatalf("DeletePosition: %v", err)
	}
	if _, err := repo.GetByID(context.Background(), created.ID); !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("GetByID after delete: want ErrPositionNotFound, got %v", err)
	}
}

func TestService_DeletePosition_NotFound(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	err := svc.DeletePosition(context.Background(), 999)
	if !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("DeletePosition: want ErrPositionNotFound, got %v", err)
	}
}

func TestService_ListPositions_Success(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	valuations := &mockValuationLookup{
		latest: make(map[int64]PositionValuation),
		ranges: make(map[int64][]PositionValuation),
	}
	svc := newTestServiceWithValuations(repo, funds, valuations)

	startDate := time.Now().UTC().Truncate(24 * time.Hour).Add(-5 * 24 * time.Hour)
	first, err := repo.Create(context.Background(), &Position{
		FundCode:         "000011",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    mustDecimal(t, "10.00"),
		CostBasis:        mustDecimal(t, "90.00"),
		EstimatedShares:  decimal.NullDecimal{Decimal: mustDecimal(t, "45.00"), Valid: true},
		HoldingDays:      2,
		HoldingStartDate: startDate,
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create first: %v", err)
	}

	second, err := repo.Create(context.Background(), &Position{
		FundCode:         "000012",
		HoldingAmount:    mustDecimal(t, "200.00"),
		HoldingProfit:    mustDecimal(t, "20.00"),
		CostBasis:        mustDecimal(t, "180.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create second: %v", err)
	}

	funds.metas["000011"] = FundMeta{Code: "000011", Name: "Alpha Fund", Type: "mixed"}
	valuations.latest[first.ID] = PositionValuation{
		PositionID:     first.ID,
		AsOf:           time.Now().UTC(),
		EstNAV:         decimal.NullDecimal{Decimal: mustDecimal(t, "1.2345"), Valid: true},
		EstChangePct:   decimal.NullDecimal{Decimal: mustDecimal(t, "0.0123"), Valid: true},
		EstMarketValue: decimal.NullDecimal{Decimal: mustDecimal(t, "101.23"), Valid: true},
		TodayPnL:       decimal.NullDecimal{Decimal: mustDecimal(t, "1.23"), Valid: true},
		Confidence:     "high",
	}

	got, err := svc.ListPositions(context.Background())
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(ListPositions) = %d, want 2", len(got))
	}
	if got[0].ID != first.ID || got[1].ID != second.ID {
		t.Fatalf("ListPositions order mismatch: got IDs [%d %d], want [%d %d]", got[0].ID, got[1].ID, first.ID, second.ID)
	}
	if got[0].FundName == nil || *got[0].FundName != "Alpha Fund" {
		t.Fatalf("FundName = %+v, want Alpha Fund", got[0].FundName)
	}
	if got[0].FundType == nil || *got[0].FundType != "mixed" {
		t.Fatalf("FundType = %+v, want mixed", got[0].FundType)
	}
	if got[0].EstNAV == nil || !got[0].EstNAV.Equal(mustDecimal(t, "1.2345")) {
		t.Fatalf("EstNAV = %+v, want 1.2345", got[0].EstNAV)
	}
	if got[0].Confidence == nil || *got[0].Confidence != "high" {
		t.Fatalf("Confidence = %+v, want high", got[0].Confidence)
	}
	if !got[0].HoldingProfitRate.Equal(mustDecimal(t, "0.1111111111111111")) {
		t.Fatalf("HoldingProfitRate = %s, want 0.1111111111111111", got[0].HoldingProfitRate)
	}
	if got[0].HoldingDays != 5 {
		t.Fatalf("HoldingDays = %d, want 5", got[0].HoldingDays)
	}
	if got[1].FundName != nil || got[1].EstNAV != nil {
		t.Fatalf("second item should degrade missing enrichment to nil, got %+v", got[1])
	}
}

func TestService_ListPositions_Empty(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	got, err := svc.ListPositions(context.Background())
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	if got == nil {
		t.Fatal("ListPositions returned nil, want empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("len(ListPositions) = %d, want 0", len(got))
	}
}

func TestService_ListPositions_FundLookupDegraded(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	funds.metaErr = errors.New("fund upstream unavailable")
	svc := newTestService(repo, funds)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000013",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    decimal.Zero,
		CostBasis:        mustDecimal(t, "100.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	got, err := svc.ListPositions(context.Background())
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	if len(got) != 1 || got[0].ID != created.ID {
		t.Fatalf("ListPositions unexpected result: %+v", got)
	}
	if got[0].FundName != nil || got[0].FundType != nil {
		t.Fatalf("fund fields should degrade to nil, got %+v", got[0])
	}
}

func TestService_ListPositions_ValuationLookupDegraded(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	valuations := &mockValuationLookup{
		latest:    make(map[int64]PositionValuation),
		latestErr: errors.New("valuation upstream unavailable"),
		ranges:    make(map[int64][]PositionValuation),
	}
	svc := newTestServiceWithValuations(repo, funds, valuations)

	if _, err := repo.Create(context.Background(), &Position{
		FundCode:         "000014",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    decimal.Zero,
		CostBasis:        mustDecimal(t, "100.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	}); err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	got, err := svc.ListPositions(context.Background())
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(ListPositions) = %d, want 1", len(got))
	}
	if got[0].EstNAV != nil || got[0].TodayPnL != nil || got[0].ValuationAsOf != nil {
		t.Fatalf("valuation fields should degrade to nil, got %+v", got[0])
	}
}

func TestService_GetPositionHistory_Success(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	valuations := &mockValuationLookup{
		latest: make(map[int64]PositionValuation),
		ranges: make(map[int64][]PositionValuation),
	}
	svc := newTestServiceWithValuations(repo, funds, valuations)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000015",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    decimal.Zero,
		CostBasis:        mustDecimal(t, "100.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	valuations.ranges[created.ID] = []PositionValuation{
		{
			PositionID:     created.ID,
			AsOf:           time.Now().UTC().Add(-2 * time.Hour),
			EstMarketValue: decimal.NullDecimal{Decimal: mustDecimal(t, "101.23"), Valid: true},
			Confidence:     "high",
		},
	}

	got, err := svc.GetPositionHistory(context.Background(), created.ID, "1d")
	if err != nil {
		t.Fatalf("GetPositionHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(GetPositionHistory) = %d, want 1", len(got))
	}
	if got[0].PositionID != created.ID {
		t.Fatalf("PositionID = %d, want %d", got[0].PositionID, created.ID)
	}
}

func TestService_GetPositionHistory_NotFound(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	_, err := svc.GetPositionHistory(context.Background(), 999, "1d")
	if !errors.Is(err, ErrPositionNotFound) {
		t.Fatalf("GetPositionHistory: want ErrPositionNotFound, got %v", err)
	}
}

func TestService_GetPositionHistory_InvalidRange(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	svc := newTestService(repo, funds)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000016",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    decimal.Zero,
		CostBasis:        mustDecimal(t, "100.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	_, err = svc.GetPositionHistory(context.Background(), created.ID, "30d")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GetPositionHistory: want ErrInvalidInput, got %v", err)
	}
}

func TestService_GetPositionHistory_ValuationDegradedToEmpty(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	valuations := &mockValuationLookup{
		latest:   make(map[int64]PositionValuation),
		rangeErr: errors.New("valuation storage unavailable"),
		ranges:   make(map[int64][]PositionValuation),
	}
	svc := newTestServiceWithValuations(repo, funds, valuations)

	created, err := repo.Create(context.Background(), &Position{
		FundCode:         "000017",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    decimal.Zero,
		CostBasis:        mustDecimal(t, "100.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	got, err := svc.GetPositionHistory(context.Background(), created.ID, "7d")
	if err != nil {
		t.Fatalf("GetPositionHistory: %v", err)
	}
	if got == nil {
		t.Fatal("GetPositionHistory returned nil, want empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("len(GetPositionHistory) = %d, want 0", len(got))
	}
}

func TestService_GetPortfolioOverview_FromSnapshot(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	valuations := &mockValuationLookup{
		latest: make(map[int64]PositionValuation),
		ranges: make(map[int64][]PositionValuation),
		portfolio: &PortfolioSnapshot{
			AsOf:           time.Now().UTC(),
			TotalAssets:    mustDecimal(t, "1234.56"),
			TodayPnL:       mustDecimal(t, "12.34"),
			TodayReturnPct: mustDecimal(t, "0.0101"),
			PositionCount:  2,
			ConfidenceSummary: map[string]int{
				"high": 1,
				"mid":  1,
			},
		},
	}
	svc := newTestServiceWithValuations(repo, funds, valuations)

	got, err := svc.GetPortfolioOverview(context.Background())
	if err != nil {
		t.Fatalf("GetPortfolioOverview: %v", err)
	}
	if got == nil {
		t.Fatal("GetPortfolioOverview returned nil")
	}
	if !got.TotalAssets.Equal(mustDecimal(t, "1234.56")) {
		t.Fatalf("TotalAssets = %s, want 1234.56", got.TotalAssets)
	}
	if !got.TodayPnL.Equal(mustDecimal(t, "12.34")) {
		t.Fatalf("TodayPnL = %s, want 12.34", got.TodayPnL)
	}
	if !got.TodayReturnPct.Equal(mustDecimal(t, "0.0101")) {
		t.Fatalf("TodayReturnPct = %s, want 0.0101", got.TodayReturnPct)
	}
	if got.PositionCount != 2 {
		t.Fatalf("PositionCount = %d, want 2", got.PositionCount)
	}
	if got.ConfidenceSummary["high"] != 1 || got.ConfidenceSummary["mid"] != 1 {
		t.Fatalf("ConfidenceSummary = %+v, want high=1 mid=1", got.ConfidenceSummary)
	}
	if got.ConfidenceSummary["low"] != 0 || got.ConfidenceSummary["unsupported"] != 0 {
		t.Fatalf("ConfidenceSummary should be seeded with zeros, got %+v", got.ConfidenceSummary)
	}
}

func TestService_GetPortfolioOverview_ImmediateCalculation(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	valuations := &mockValuationLookup{
		latest: make(map[int64]PositionValuation),
		ranges: make(map[int64][]PositionValuation),
	}
	svc := newTestServiceWithValuations(repo, funds, valuations)

	first, err := repo.Create(context.Background(), &Position{
		FundCode:         "000018",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    mustDecimal(t, "10.00"),
		CostBasis:        mustDecimal(t, "90.00"),
		HoldingDays:      3,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create first: %v", err)
	}
	second, err := repo.Create(context.Background(), &Position{
		FundCode:         "000019",
		HoldingAmount:    mustDecimal(t, "200.00"),
		HoldingProfit:    mustDecimal(t, "20.00"),
		CostBasis:        mustDecimal(t, "180.00"),
		HoldingDays:      5,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	})
	if err != nil {
		t.Fatalf("repo.Create second: %v", err)
	}

	valuations.latest[first.ID] = PositionValuation{
		PositionID:     first.ID,
		EstMarketValue: decimal.NullDecimal{Decimal: mustDecimal(t, "101.23"), Valid: true},
		TodayPnL:       decimal.NullDecimal{Decimal: mustDecimal(t, "1.23"), Valid: true},
		Confidence:     "high",
	}
	valuations.latest[second.ID] = PositionValuation{
		PositionID:     second.ID,
		EstMarketValue: decimal.NullDecimal{Decimal: mustDecimal(t, "205.00"), Valid: true},
		TodayPnL:       decimal.NullDecimal{Decimal: mustDecimal(t, "-0.50"), Valid: true},
		Confidence:     "low",
	}

	got, err := svc.GetPortfolioOverview(context.Background())
	if err != nil {
		t.Fatalf("GetPortfolioOverview: %v", err)
	}
	if got == nil {
		t.Fatal("GetPortfolioOverview returned nil")
	}
	if !got.TotalAssets.Equal(mustDecimal(t, "306.23")) {
		t.Fatalf("TotalAssets = %s, want 306.23", got.TotalAssets)
	}
	if !got.TodayPnL.Equal(mustDecimal(t, "0.73")) {
		t.Fatalf("TodayPnL = %s, want 0.73", got.TodayPnL)
	}
	if !got.TodayReturnPct.Equal(mustDecimal(t, "0.0023895253682488")) {
		t.Fatalf("TodayReturnPct = %s, want 0.0023895253682488", got.TodayReturnPct)
	}
	if got.PositionCount != 2 {
		t.Fatalf("PositionCount = %d, want 2", got.PositionCount)
	}
	if got.ConfidenceSummary["high"] != 1 || got.ConfidenceSummary["low"] != 1 {
		t.Fatalf("ConfidenceSummary = %+v, want high=1 low=1", got.ConfidenceSummary)
	}
	if got.AsOf.IsZero() {
		t.Fatal("AsOf is zero, want fallback timestamp")
	}
}

func TestService_GetPortfolioOverview_ImmediateCalculationValuationDegraded(t *testing.T) {
	repo := newMockRepository()
	funds := newMockFundLookup()
	valuations := &mockValuationLookup{
		latest:    make(map[int64]PositionValuation),
		latestErr: errors.New("valuation cache unavailable"),
		ranges:    make(map[int64][]PositionValuation),
	}
	svc := newTestServiceWithValuations(repo, funds, valuations)

	if _, err := repo.Create(context.Background(), &Position{
		FundCode:         "000020",
		HoldingAmount:    mustDecimal(t, "100.00"),
		HoldingProfit:    mustDecimal(t, "10.00"),
		CostBasis:        mustDecimal(t, "90.00"),
		HoldingDays:      1,
		HoldingStartDate: time.Now().UTC().Truncate(24 * time.Hour),
		Source:           SourceManual,
	}); err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	got, err := svc.GetPortfolioOverview(context.Background())
	if err != nil {
		t.Fatalf("GetPortfolioOverview: %v", err)
	}
	if got == nil {
		t.Fatal("GetPortfolioOverview returned nil")
	}
	if !got.TotalAssets.Equal(decimal.Zero) || !got.TodayPnL.Equal(decimal.Zero) || !got.TodayReturnPct.Equal(decimal.Zero) {
		t.Fatalf("fallback degraded totals should be zero, got %+v", got)
	}
	if got.PositionCount != 1 {
		t.Fatalf("PositionCount = %d, want 1", got.PositionCount)
	}
	if got.ConfidenceSummary["high"] != 0 || got.ConfidenceSummary["unsupported"] != 0 {
		t.Fatalf("ConfidenceSummary should remain zeroed, got %+v", got.ConfidenceSummary)
	}
}
