// Package errors 实现 FR-PL-09：统一响应壳与错误码雏形。
//
// 完整码表见 REQ-06；本包只锁定**形状**与基础码，业务层（asset/fund/market/valuation）
// 在自身包内追加专属码（如 POSITION_DUPLICATE / FUND_NOT_FOUND），实现方式：
// 直接 NewError("POSITION_DUPLICATE", ...) 即可，本包不维护中央枚举。
//
// 用法：
//
//	errors.WriteOK(w, r, data)
//	errors.WriteError(w, r, errors.ErrNotFound("position not found"))
//	errors.WriteError(w, r, errors.New("POSITION_DUPLICATE", http.StatusConflict, "fund already exists"))
package errors

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

// Envelope 是所有 JSON 响应的统一外壳（REQ-01 FR-PL-09）。
type Envelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	Meta    Meta   `json:"meta"`
}

// Meta 至少携带 trace_id；后续可在此处加 pagination 等。
type Meta struct {
	TraceID string `json:"trace_id"`
}

// Error 是带错误码 + HTTP 状态的标准错误；业务层通过 New 构造。
type Error struct {
	Code       string // 大写下划线，如 POSITION_DUPLICATE
	HTTPStatus int    // 4xx / 5xx
	Message    string // 面向调用方的可读信息
	Cause      error  // 内部原因，不会序列化进响应，仅用于日志
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// New 构造业务错误。
func New(code string, httpStatus int, msg string) *Error {
	return &Error{Code: code, HTTPStatus: httpStatus, Message: msg}
}

// Wrap 在 New 基础上附加底层 cause，便于日志追溯。
func Wrap(code string, httpStatus int, msg string, cause error) *Error {
	return &Error{Code: code, HTTPStatus: httpStatus, Message: msg, Cause: cause}
}

// 基础码（FR-PL-09 列举的通用码）。
const (
	CodeOK                 = "OK"
	CodeInvalidInput       = "INVALID_INPUT"
	CodeNotFound           = "NOT_FOUND"
	CodeInternalError      = "INTERNAL_ERROR"
	CodeUpstreamUnavailable = "UPSTREAM_UNAVAILABLE"
)

// 基础构造助手。
func ErrInvalidInput(msg string) *Error {
	return New(CodeInvalidInput, http.StatusBadRequest, msg)
}
func ErrNotFound(msg string) *Error {
	return New(CodeNotFound, http.StatusNotFound, msg)
}
func ErrInternal(cause error) *Error {
	return Wrap(CodeInternalError, http.StatusInternalServerError, "internal error", cause)
}
func ErrUpstream(msg string, cause error) *Error {
	return Wrap(CodeUpstreamUnavailable, http.StatusBadGateway, msg, cause)
}

// WriteJSON 以指定 HTTP 状态写出 Envelope。data 可为 nil。
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, code, message string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	env := Envelope{
		Code:    code,
		Message: message,
		Data:    data,
		Meta:    Meta{TraceID: logger.TraceIDFromContext(r.Context())},
	}
	if err := json.NewEncoder(w).Encode(env); err != nil {
		// 极端情况：连响应都写不出，只能记日志
		logger.FromContext(r.Context()).Error("write json failed", "err", err.Error())
	}
}

// WriteOK 200 + CodeOK 快捷。
func WriteOK(w http.ResponseWriter, r *http.Request, data any) {
	WriteJSON(w, r, http.StatusOK, CodeOK, "", data)
}

// WriteError 把任何 error 序列化为响应：
//   - *Error  → 使用其 Code/HTTPStatus/Message
//   - 其它     → 视作 INTERNAL_ERROR
//
// 同时把原始 cause 写日志，避免吞错。
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = ErrInternal(err)
	}
	if e.Cause != nil {
		logger.FromContext(r.Context()).Error("request failed",
			"code", e.Code,
			"http_status", e.HTTPStatus,
			"cause", e.Cause.Error(),
		)
	} else {
		logger.FromContext(r.Context()).Warn("request rejected",
			"code", e.Code,
			"http_status", e.HTTPStatus,
			"message", e.Message,
		)
	}
	WriteJSON(w, r, e.HTTPStatus, e.Code, e.Message, nil)
}
