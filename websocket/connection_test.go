package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
)

func testUpgrader(t *testing.T) *WebSocketUpgrader {
	t.Helper()
	u := NewWebSocketUpgrader(nil)
	t.Cleanup(func() { u.manager.Shutdown(context.Background()) })
	return u
}

func testPair(t *testing.T, u *WebSocketUpgrader) (*Connection, *coderws.Conn) {
	t.Helper()
	accepted := make(chan *Connection, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r)
		if err != nil {
			t.Errorf("Upgrade: %v", err)
			return
		}
		accepted <- conn
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case conn := <-accepted:
		t.Cleanup(func() { client.CloseNow(); conn.CloseNow() })
		return conn, client
	case <-ctx.Done():
		client.CloseNow()
		t.Fatal("upgrade did not finish")
		return nil, nil
	}
}

func TestDisconnectedSocketReleasesCapacity(t *testing.T) {
	u := testUpgrader(t)
	u.manager.SetMaxConns(1)
	conn, client := testPair(t, u)
	client.CloseNow()
	if _, err := conn.ReadText(); err == nil {
		t.Fatal("expected peer EOF")
	}
	if !conn.IsClosed() || u.manager.Count() != 0 {
		t.Fatalf("dead socket retained: closed=%v count=%d", conn.IsClosed(), u.manager.Count())
	}
	last := conn.lastActivity.Load()
	if err := conn.SendText("failed"); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("SendText: %v", err)
	}
	u.manager.BroadcastText("failed broadcast must not revive a connection")
	if conn.lastActivity.Load() != last {
		t.Fatal("failed operation refreshed activity")
	}
	testPair(t, u) // the only capacity slot must now be reusable
}

func TestEncodingErrorKeepsSocketUsable(t *testing.T) {
	u := testUpgrader(t)
	conn, client := testPair(t, u)
	last := conn.lastActivity.Load()
	if err := conn.Send(make(chan int)); err == nil {
		t.Fatal("expected JSON encoding error")
	}
	if conn.IsClosed() || conn.lastActivity.Load() != last {
		t.Fatal("encoding error changed connection lifecycle")
	}
	if err := conn.Send(map[string]string{"message": "hello"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, body, err := client.Read(ctx)
	if err != nil || string(body) != `{"message":"hello"}` {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestOperationDeadlineIncludesLockWait(t *testing.T) {
	u := testUpgrader(t)
	conn, _ := testPair(t, u)
	conn.writeTimeout = 40 * time.Millisecond
	conn.writeGate <- struct{}{}
	last := conn.lastActivity.Load()
	started := time.Now()
	err := conn.SendText("waiting")
	<-conn.writeGate
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("deadline did not bound queued send: %v", err)
	}
	if conn.IsClosed() || conn.lastActivity.Load() != last {
		t.Fatal("lock-wait timeout changed a healthy socket")
	}
	conn.readTimeout = 40 * time.Millisecond
	conn.readGate <- struct{}{}
	_, err = conn.ReadText()
	<-conn.readGate
	if !errors.Is(err, context.DeadlineExceeded) || conn.IsClosed() {
		t.Fatalf("queued read: %v closed=%v", err, conn.IsClosed())
	}
}

func TestShutdownDeadlineClosesUnresponsivePeers(t *testing.T) {
	u := testUpgrader(t)
	first, _ := testPair(t, u)
	second, _ := testPair(t, u)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := u.manager.ShutdownWithContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown exceeded total deadline: %s", elapsed)
	}
	for _, conn := range []*Connection{first, second} {
		select {
		case <-conn.closeDone:
		default:
			t.Fatal("shutdown returned before transport cleanup")
		}
	}
	if u.manager.Count() != 0 {
		t.Fatal("shutdown retained connections")
	}
}

func TestShutdownRejectsUpgrade(t *testing.T) {
	u := testUpgrader(t)
	u.manager.Shutdown(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = u.Upgrade(w, r) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, response, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("post-shutdown upgrade response=%v err=%v", response, err)
	}
}

func TestCleanupStartsOnUseAndStopsWhenEmpty(t *testing.T) {
	u := testUpgrader(t)
	if u.manager.cleanupStop != nil {
		t.Fatal("unused manager started background cleanup")
	}
	conn, client := testPair(t, u)
	u.manager.mu.RLock()
	started := u.manager.cleanupStop != nil
	u.manager.mu.RUnlock()
	if !started {
		t.Fatal("active manager did not start cleanup")
	}
	client.CloseNow()
	conn.ReadText()
	u.manager.mu.RLock()
	stopped := u.manager.cleanupStop == nil
	u.manager.mu.RUnlock()
	if !stopped {
		t.Fatal("empty manager kept its cleanup cycle")
	}
	u.manager.cleanup.Wait()
	// Retain the existing public default; idle chat servers can explicitly use zero.
	if DefaultConfig().ReadTimeout != 30 {
		t.Fatal("default read timeout changed")
	}
}

func TestIdleCleanupClosesConnection(t *testing.T) {
	u := testUpgrader(t)
	u.manager.SetTimeout(1)
	conn, client := testPair(t, u)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client.CloseRead(ctx)
	select {
	case <-conn.closeDone:
	case <-ctx.Done():
		t.Fatal("idle connection was not cleaned")
	}
	if u.manager.Count() != 0 {
		t.Fatal("idle connection still counted")
	}
}

func TestPingUpdatesActivity(t *testing.T) {
	u := testUpgrader(t)
	conn, client := testPair(t, u)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client.CloseRead(ctx)
	readDone := make(chan struct{})
	go func() { defer close(readDone); _, _ = conn.ReadText() }()
	old := time.Now().Add(-time.Hour).UnixNano()
	conn.lastActivity.Store(old)
	if err := client.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if conn.lastActivity.Load() <= old {
		t.Fatal("received ping did not refresh idle activity")
	}
	conn.CloseNow()
	select {
	case <-readDone:
	case <-ctx.Done():
		t.Fatal("read did not exit when connection closed")
	}
}

type countedJSON struct{ calls *atomic.Int32 }

func (m countedJSON) MarshalJSON() ([]byte, error) {
	m.calls.Add(1)
	return []byte(`{"message":"broadcast"}`), nil
}

func TestBroadcastEncodesOnceAndDoesNotBlockHealthyPeer(t *testing.T) {
	u := testUpgrader(t)
	slow, _ := testPair(t, u)
	_, healthy := testPair(t, u)
	slow.writeGate <- struct{}{}
	defer func() { <-slow.writeGate }()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	result := make(chan error, 1)
	go func() { result <- u.manager.BroadcastContext(ctx, countedJSON{&calls}) }()
	readCtx, readCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer readCancel()
	_, body, err := healthy.Read(readCtx)
	if err != nil || !json.Valid(body) {
		t.Fatalf("healthy peer delayed by slow peer: %v body=%s", err, body)
	}
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow peer deadline: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("JSON encoded %d times", calls.Load())
	}
}

func TestBroadcastBackpressurePrecedesEncoding(t *testing.T) {
	u := testUpgrader(t)
	u.manager.broadcastGate <- struct{}{}
	defer func() { <-u.manager.broadcastGate }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	err := u.manager.BroadcastContext(ctx, countedJSON{&calls})
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatalf("queued batch err=%v encode count=%d", err, calls.Load())
	}
}
