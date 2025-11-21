package client

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

// ClientError 客户端错误类型
type ClientError struct {
	Code    int
	Message string
	Err     error
}

// Error 实现 error 接口
func (e *ClientError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Unwrap 实现 errors.Unwrap 接口
func (e *ClientError) Unwrap() error {
	return e.Err
}

// NewError 创建客户端错误
func NewError(code int, message string, err error) *ClientError {
	return &ClientError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// 预定义错误
var (
	ErrNotAuthenticated = &ClientError{
		Code:    ErrCodeNotAuthenticated,
		Message: "authentication required",
	}

	ErrAuthFailed = &ClientError{
		Code:    ErrCodeAuthFailed,
		Message: "authentication failed",
	}

	ErrTimeout = &ClientError{
		Code:    ErrCodeTimeout,
		Message: "request timeout",
	}

	ErrInvalidRequest = &ClientError{
		Code:    ErrCodeInvalidRequest,
		Message: "invalid request",
	}

	ErrInternalError = &ClientError{
		Code:    ErrCodeInternalError,
		Message: "internal error",
	}

	ErrConnectionClosed = &ClientError{
		Code:    ErrCodeConnectionClosed,
		Message: "connection closed",
	}

	ErrBadProtocol = &ClientError{
		Code:    ErrCodeBadProtocol,
		Message: "bad protocol",
	}
)

// IsClientError 检查是否为客户端错误
func IsClientError(err error) bool {
	var clientErr *ClientError
	return errors.As(err, &clientErr)
}

// GetErrorCode 获取错误代码
func GetErrorCode(err error) int {
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return clientErr.Code
	}
	return ErrCodeUnknown
}

// WrapError 包装错误为客户端错误
func WrapError(code int, message string, err error) error {
	if err == nil {
		return nil
	}
	return NewError(code, message, err)
}
