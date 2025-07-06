package websocket

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

var (
	// ErrConnectionClosed 连接已关闭错误
	ErrConnectionClosed = errors.New("websocket connection is closed")
	// ErrInvalidMessage 无效消息错误
	ErrInvalidMessage = errors.New("invalid websocket message")
	// ErrMessageTooLarge 消息过大错误
	ErrMessageTooLarge = errors.New("websocket message too large")
	// ErrUpgradeFailed 升级失败错误
	ErrUpgradeFailed = errors.New("websocket upgrade failed")
)

// generateID 生成唯一的连接ID
func generateID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
