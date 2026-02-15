package asset

import (
	"net/http"

	apperrors "github.com/namelyzz/FundPilot/internal/platform/errors"
)

// 错误码遵循 REQ-01 FR-PL-09：基础码（INVALID_INPUT / NOT_FOUND / INTERNAL_ERROR）
// 走 platform/errors；本包按 platform/errors 包注释的约定追加 asset 域专属码。
//
// 这些 var 是指针 sentinel：实现侧命中相应场景时必须直接 `return ErrXxx`
// （而不是用 apperrors.New 重新构造），调用方才能用 errors.Is 精确分流。
// 需要附加底层 cause 时，用 fmt.Errorf("…: %w", ErrXxx) 包一层即可。
//
// 分两组：
//   - 本域自有：ErrPositionDuplicate / ErrPositionNotFound 由 asset.PositionRepo 直接 return
//   - 跨域契约：ErrFundNotFound / ErrValuationNotFound 是 [FundLookup] / [ValuationLookup]
//     的"未找到"约定，由 REQ-03 / REQ-05 的实现侧 return，asset.Service 据此降级
//     （详见 ports.go 内各接口方法注释）
var (
	// ErrPositionDuplicate 在创建持仓时 fund_code 已存在；
	// 对应 FR-AS-01 的 POSITION_DUPLICATE / HTTP 409（spec §8）。
	ErrPositionDuplicate = apperrors.New(
		"POSITION_DUPLICATE",
		http.StatusConflict,
		"持仓已存在：fund_code 不可重复",
	)

	// ErrPositionNotFound 在按 id / fund_code 查询、更新或删除持仓时找不到记录；
	// 对应 FR-AS-02 / FR-AS-03 的 POSITION_NOT_FOUND / HTTP 404（spec §8）。
	ErrPositionNotFound = apperrors.New(
		"POSITION_NOT_FOUND",
		http.StatusNotFound,
		"持仓不存在",
	)

	// ErrPositionVersionConflict 在 UPDATE 时数据库行的 version 与调用方读出的
	// 不一致，说明在 read-modify-write 期间有其它请求改过同一行。
	// 服务端不重试，直接 409；由调用方刷新后重新发起 PATCH。
	ErrPositionVersionConflict = apperrors.New(
		"POSITION_VERSION_CONFLICT",
		http.StatusConflict,
		"持仓已被其它请求修改，请刷新后重试",
	)

	// ErrFundNotFound 是 [FundLookup.LatestNAV] 在 fund_code 未入库时的契约返回。
	// asset.Service 收到后降级（如 estimated_shares 置 NULL），不向用户报错。
	// 仅作为内部 sentinel；不直接出现在 HTTP 响应里。
	ErrFundNotFound = apperrors.New(
		"FUND_NOT_FOUND",
		http.StatusNotFound,
		"基金信息不存在",
	)

	// ErrValuationNotFound LatestBatch / Range 用空集合表达'无数据'；
	// 仅 PortfolioOverview 这种'必须有一个结果'的场景才返回此 sentinel。
	ErrValuationNotFound = apperrors.New(
		"VALUATION_NOT_FOUND",
		http.StatusNotFound,
		"估值数据不存在",
	)
)
