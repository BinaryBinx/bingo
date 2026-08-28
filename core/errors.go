package core

import (
	"errors"
	"fmt"
)

// 定义错误类型
var (
	// ErrInvalidConfig 无效配置错误
	ErrInvalidConfig = errors.New("invalid configuration")
	// ErrServerStart 服务器启动错误
	ErrServerStart = errors.New("server start failed")
	// ErrRouteNotFound 路由未找到错误
	ErrRouteNotFound = errors.New("route not found")
	// ErrMethodNotAllowed 方法不允许错误
	ErrMethodNotAllowed = errors.New("method not allowed")
	// ErrInternalServer 内部服务器错误
	ErrInternalServer = errors.New("internal server error")
	// ErrBadRequest 错误请求
	ErrBadRequest = errors.New("bad request")
	// ErrUnauthorized 未授权错误
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden 禁止访问错误
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound 资源未找到错误
	ErrNotFound = errors.New("resource not found")
)

// AppError 应用错误结构
type AppError struct {
	Err     error
	Message string
	Code    int
}

// Error 实现error接口
func (e *AppError) Error() string {
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

// NewAppError 创建新的应用错误
func NewAppError(err error, message string, code int) *AppError {
	return &AppError{
		Err:     err,
		Message: message,
		Code:    code,
	}
}

// ErrorResponse 错误响应结构
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// SendError 发送错误响应
func (c *RequestContext) SendError(statusCode int, err error, message string) {
	// 防御 nil error，避免 err.Error() 直接 panic
	if err == nil {
		err = errors.New("unknown error")
	}
	c.JSON(statusCode, ErrorResponse{
		Error:   err.Error(),
		Message: message,
		Code:    statusCode,
	})
}
