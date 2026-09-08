package websocket

import (
	"context"
	coderws "github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func reviewPair(t *testing.T) (*WebSocketUpgrader, *Connection, *coderws.Conn) {
	u := NewWebSocketUpgrader(nil)
	accepted := make(chan *Connection, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r)
		if err == nil {
			accepted <- conn
		}
	}))
	t.Cleanup(s.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var conn *Connection
	select {
	case conn = <-accepted:
	case <-ctx.Done():
		t.Fatal("no upgrade")
	}
	t.Cleanup(func() { client.CloseNow(); conn.CloseNow(); u.manager.Shutdown(context.Background()) })
	return u, conn, client
}

func TestReviewWebSocketTimeoutRetainsDeadConnection(t *testing.T) {
	u, conn, _ := reviewPair(t)
	conn.readTimeout = 20 * time.Millisecond
	_, err := conn.ReadText()
	t.Logf("read=%v closed=%v count=%d", err, conn.IsClosed(), u.manager.Count())
	if !conn.IsClosed() || u.manager.Count() != 0 {
		t.Fatal("timed-out transport retained in manager")
	}
}

func TestReviewMalformedJSONRetainsDeadConnection(t *testing.T) {
	u, conn, client := reviewPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Write(ctx, coderws.MessageText, []byte("invalid json")); err != nil {
		t.Fatal(err)
	}
	client.CloseRead(ctx)
	var v any
	err := conn.Read(&v)
	if !conn.IsClosed() || u.manager.Count() != 0 {
		t.Fatalf("malformed JSON err=%v closed=%v count=%d", err, conn.IsClosed(), u.manager.Count())
	}
}

func TestReviewWebSocketShutdownDeadline(t *testing.T) {
	u, _, _ := reviewPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	u.manager.Shutdown(ctx)
	elapsed := time.Since(start)
	t.Logf("50ms shutdown deadline completed in %v", elapsed)
	if elapsed > time.Second {
		t.Fatalf("50ms shutdown deadline took %v", elapsed)
	}
}

func TestReviewWebSocketLockWaitDeadline(t *testing.T) {
	_, conn, _ := reviewPair(t)
	conn.writeTimeout = 20 * time.Millisecond
	conn.writeGate <- struct{}{}
	done := make(chan error, 1)
	go func() { done <- conn.SendText("x") }()
	select {
	case <-done:
		<-conn.writeGate
		return
	case <-time.After(100 * time.Millisecond):
		t.Error("20ms write timeout did not cover lock wait")
	}
	<-conn.writeGate
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("write failed to finish")
	}
}
