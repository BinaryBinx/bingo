package main

import (
	"encoding/json"
	"github.com/BinaryBinx/bingo/core"
	"github.com/fasthttp/websocket"
	"net"
	"sync"
	"testing"
	"time"
)

func TestReviewConcurrentChat(t *testing.T) {
	cfg := core.DefaultConfig()
	cfg.RunMode = core.RunModeTest
	app := core.NewApp(cfg)
	room := NewChatRoom(app)
	app.GET("/ws", room.HandleWebSocket)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go app.Serve(ln)
	defer app.Shutdown()
	var clients []*websocket.Conn
	received := make(chan struct{}, 3)
	for i := 0; i < 3; i++ {
		conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		defer conn.Close()
		go func(c *websocket.Conn) {
			notified := false
			for {
				_, payload, err := c.ReadMessage()
				if err != nil {
					return
				}
				var message ChatMessage
				if json.Unmarshal(payload, &message) == nil && message.Type == "message" && !notified {
					received <- struct{}{}
					notified = true
				}
			}
		}(conn)
	}
	data, _ := json.Marshal(ChatMessage{Type: "message", Message: "concurrent"})
	var wg sync.WaitGroup
	for _, conn := range clients {
		wg.Go(func() {
			for i := 0; i < 20; i++ {
				conn.SetWriteDeadline(time.Now().Add(time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
			}
		})
	}
	wg.Wait()
	for i := 0; i < 3; i++ {
		select {
		case <-received:
		case <-time.After(2 * time.Second):
			t.Fatal("broadcast did not reach every client")
		}
	}
}
