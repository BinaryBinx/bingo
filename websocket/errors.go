package websocket

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"time"
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

var fallbackIDCounter uint64

// generateID 生成唯一的连接ID
func generateID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		id := atomic.AddUint64(&fallbackIDCounter, 1)
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405"))) + hex.EncodeToString([]byte{byte(id), byte(id >> 8), byte(id >> 16), byte(id >> 24)})
	}
	return hex.EncodeToString(bytes)
}
