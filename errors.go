package cserver

import (
	"errors"
	"fmt"
)

// 错误代码常量
const (
	ErrCodeUnknown          = 0
	ErrCodeNotAuthenticated = 1
	ErrCodeAuthFailed       = 2
	ErrCodeNoHandler        = 3
	ErrCodeTimeout          = 4
	ErrCodeRateLimit        = 5
	ErrCodeInvalidRequest   = 6
	ErrCodeInternalError    = 7
	ErrCodeConnectionClosed = 8
	ErrCodeMaxConnections   = 9
	ErrCodeBadProtocol      = 10
)

// ServerError 服务器错误类型
type ServerError struct {
	Code    int
	Message string
	Err     error
}

// Error 实现 error 接口
func (e *ServerError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Unwrap 实现 errors.Unwrap 接口
func (e *ServerError) Unwrap() error {
	return e.Err
}

// NewError 创建服务器错误
func NewError(code int, message string, err error) *ServerError {
	return &ServerError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// 预定义错误
var (
	ErrNotAuthenticated = &ServerError{
		Code:    ErrCodeNotAuthenticated,
		Message: "authentication required",
	}

	ErrAuthFailed = &ServerError{
		Code:    ErrCodeAuthFailed,
		Message: "authentication failed",
	}

	ErrNoHandler = &ServerError{
		Code:    ErrCodeNoHandler,
		Message: "no handler registered for this command",
	}

	ErrTimeout = &ServerError{
		Code:    ErrCodeTimeout,
		Message: "request timeout",
	}

	ErrRateLimit = &ServerError{
		Code:    ErrCodeRateLimit,
		Message: "rate limit exceeded",
	}

	ErrInvalidRequest = &ServerError{
		Code:    ErrCodeInvalidRequest,
		Message: "invalid request",
	}

	ErrInternalError = &ServerError{
		Code:    ErrCodeInternalError,
		Message: "internal server error",
	}

	ErrConnectionClosed = &ServerError{
		Code:    ErrCodeConnectionClosed,
		Message: "connection closed",
	}

	ErrMaxConnections = &ServerError{
		Code:    ErrCodeMaxConnections,
		Message: "maximum connections reached",
	}

	ErrBadProtocol = &ServerError{
		Code:    ErrCodeBadProtocol,
		Message: "bad protocol",
	}
)

// IsServerError 检查是否为服务器错误
func IsServerError(err error) bool {
	var serverErr *ServerError
	return errors.As(err, &serverErr)
}

// GetErrorCode 获取错误代码
func GetErrorCode(err error) int {
	var serverErr *ServerError
	if errors.As(err, &serverErr) {
		return serverErr.Code
	}
	return ErrCodeUnknown
}

// WrapError 包装错误为服务器错误
func WrapError(code int, message string, err error) error {
	if err == nil {
		return nil
	}
	return NewError(code, message, err)
}
