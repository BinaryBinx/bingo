package main

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/core"

	"github.com/fasthttp/websocket"
)

// TestWebSocketHandshake 验证 fasthttp 原生 WebSocket 升级：
// 真实 TCP 服务 + 标准客户端握手，确认 /ws 不再因适配器缺陷 panic
func TestWebSocketHandshake(t *testing.T) {
	app := core.NewApp(nil)
	app.SetRunMode(core.RunModeRelease)

	chatRoom := NewChatRoom(app)
	app.GET("/ws", func(ctx *core.RequestContext) {
		chatRoom.HandleWebSocket(ctx)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- app.Serve(ln)
	}()
	defer func() {
		_ = app.Shutdown()
		<-serveDone
	}()

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatalf("websocket handshake failed: %v", err)
	}
	defer conn.Close()

	// 握手成功后应能收到消息（join 广播 + 系统欢迎），验证消息循环可用
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first message failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("received empty message")
	}
	t.Logf("received: %s", data)

	// 再读一条（系统欢迎消息）
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read second message failed: %v", err)
	}
	t.Logf("received: %s", data)
	// Application heartbeats are answered privately instead of broadcast as chat.
	if err := conn.WriteJSON(ChatMessage{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var pong ChatMessage
	if err := json.Unmarshal(data, &pong); err != nil || pong.Type != "pong" {
		t.Fatalf("heartbeat response=%s err=%v", data, err)
	}
	// The room now uses the upstream upgrader's same-origin policy.
	foreign, response, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", http.Header{"Origin": {"https://foreign.test"}})
	if foreign != nil {
		foreign.Close()
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("cross-origin handshake accepted")
	}
	if err := app.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = conn.ReadMessage(); err == nil {
		t.Fatal("hijacked connection survived application shutdown")
	}
	chatRoom.mu.RLock()
	users := len(chatRoom.users)
	chatRoom.mu.RUnlock()
	if users != 0 {
		t.Fatalf("shutdown retained %d users", users)
	}
}
