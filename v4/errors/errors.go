package errors

import (
	"errors"
	"fmt"
)

type CodeError struct {
	Code    int
	Message string
	Err     error
}

// Error 返回带错误码的可读文本（用于日志与对外错误描述）。
func (e *CodeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("%d %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%d %s: %v", e.Code, e.Message, e.Err)
}

// Unwrap 返回底层错误，便于 errors.Is/errors.As 继续判断。
func (e *CodeError) Unwrap() error { return e.Err }

// New 构造一个仅包含错误码与消息的 CodeError。
// 参数：
// - code: 错误码
// - msg: 错误描述
func New(code int, msg string) *CodeError { return &CodeError{Code: code, Message: msg} }

// Wrap 将底层错误包装为带错误码的 CodeError。
// 参数：
// - code: 错误码
// - msg: 错误描述
// - err: 底层错误（可为 nil）
func Wrap(code int, msg string, err error) *CodeError {
	if err == nil {
		return &CodeError{Code: code, Message: msg}
	}
	return &CodeError{Code: code, Message: msg, Err: err}
}

// WithMessage 为错误追加上下文消息。
// 规则：
// - 若 err 为 CodeError，则保留 code，仅替换 message 并保留底层 err
// - 否则使用 fmt.Errorf("%s: %w", ...) 保留可追溯性
func WithMessage(err error, msg string) error {
	if err == nil {
		return nil
	}
	var ce *CodeError
	if errors.As(err, &ce) {
		return &CodeError{Code: ce.Code, Message: msg, Err: ce.Err}
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// Code 提取错误码。
// 返回：
// - 0: err 为 nil
// - CodeError: 返回其中的 Code
// - 其它错误: 默认返回 500
func Code(err error) int {
	if err == nil {
		return 0
	}
	var ce *CodeError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return 500
}

const (
	CodeInternal      = 500
	CodeAuthFailed    = 501
	CodeBadRequest    = 502
	CodeConflict      = 503
	CodeUnavailable   = 504
	CodeNoPortTimeout = 505
)
