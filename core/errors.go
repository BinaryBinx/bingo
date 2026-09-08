package core

import (
	"errors"
	"fmt"

	"github.com/BinaryBinx/bingo/internal/responsemeta"
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

// Unwrap 暴露内部错误，支持 errors.Is / errors.As 错误链遍历
func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
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

// SendError 发送 JSON 错误响应，清理旧正文的编码、校验器和缓存策略，
// 保留 CORS、安全、请求追踪及认证/重试头。
func (c *RequestContext) SendError(statusCode int, err error, message string) {
	// 防御 nil error，避免 err.Error() 直接 panic
	if err == nil {
		err = errors.New("unknown error")
	}
	response := ErrorResponse{
		Error:   err.Error(),
		Message: message,
		Code:    statusCode,
	}
	responsemeta.ResetError(c.RequestCtx, statusCode, "")
	if jsonErr := c.JSON(statusCode, response); jsonErr != nil {
		resetErrorResponse(c.RequestCtx)
	}
}
