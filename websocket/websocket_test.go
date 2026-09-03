package websocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
)

// newTestConnection 构造仅用于管理器测试的伪连接（无底层 conn）
func newTestConnection(manager *ConnectionManager) *Connection {
	c := &Connection{
		manager: manager,
		id:      generateID(),
		cancel:  func() {},
	}
	c.lastActivity.Store(time.Now().UnixNano())
	return c
}

// TestManagerRejectsAddAfterShutdown Shutdown 后不得再接受新连接
func TestManagerRejectsAddAfterShutdown(t *testing.T) {
	m := NewConnectionManager()
	c1 := newTestConnection(m)
	if !m.Add(c1) {
		t.Fatal("expected add to succeed before shutdown")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.Shutdown(ctx)

	c2 := newTestConnection(m)
	if m.Add(c2) {
		t.Fatal("expected add to fail after shutdown")
	}
	if m.Count() != 0 {
		t.Fatalf("expected all connections cleared, got %d", m.Count())
	}
}

// TestManagerShutdownBounded Shutdown 应在期限内完成，不因串行关闭握手拖住退出
func TestManagerShutdownBounded(t *testing.T) {
	m := NewConnectionManager()
	for i := 0; i < 40; i++ {
		c := newTestConnection(m)
		if !m.Add(c) {
			t.Fatal("failed to add test connection")
		}
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.Shutdown(ctx)

	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("shutdown took too long: %v", elapsed)
	}
	if m.Count() != 0 {
		t.Fatalf("expected connections cleared, got %d", m.Count())
	}
}

// TestManagerShutdownIdempotent 重复 Shutdown 是幂等的且快速返回
func TestManagerShutdownIdempotent(t *testing.T) {
	m := NewConnectionManager()
	m.Shutdown(context.Background())

	done := make(chan struct{})
	go func() {
		m.Shutdown(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second shutdown must return immediately")
	}
}

// TestTerminalErrorDetection 终止性错误分类：
// EOF/CloseError 应清理连接；超时与 JSON 编解码错误不清理
func TestTerminalErrorDetection(t *testing.T) {
	if isTerminalConnError(nil) {
		t.Fatal("nil must not be terminal")
	}
	if !isTerminalConnError(fmt.Errorf("read: %w", io.EOF)) {
		t.Fatal("EOF must be terminal")
	}
	coderErr := fmt.Errorf("closed: %w", coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "bye"})
	if !isTerminalConnError(coderErr) {
		t.Fatal("CloseError must be terminal")
	}
	if isTerminalConnError(context.DeadlineExceeded) {
		t.Fatal("timeout must not be terminal")
	}
	if isTerminalConnError(errors.New("json: unsupported type")) {
		t.Fatal("JSON encode error must not be terminal")
	}
}
