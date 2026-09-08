package websocket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
)

func TestCloseNowInterruptsExistingHandshake(t *testing.T) {
	u, c, _ := reviewPair(t)
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	deadline := time.Now().Add(time.Second)
	for !c.IsClosed() {
		if time.Now().After(deadline) {
			t.Fatal("Close never started")
		}
		time.Sleep(time.Millisecond)
	}
	if u.manager.Count() != 1 {
		t.Fatal("closing connection removed before transport completion")
	}
	started := time.Now()
	c.CloseNow()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("handshake not interrupted")
	}
	if time.Since(started) > time.Second || u.manager.Count() != 0 {
		t.Fatal("force close did not finish")
	}
}

func TestManagerShutdownManyUnresponsivePeers(t *testing.T) {
	u := NewWebSocketUpgrader(nil)
	accepted := make(chan *Connection, 130)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := u.Upgrade(w, r)
		if err == nil {
			accepted <- c
		}
	}))
	defer s.Close()
	var clients []*coderws.Conn
	defer func() {
		for _, c := range clients {
			c.CloseNow()
		}
		u.manager.Shutdown(context.Background())
	}()
	for i := 0; i < 130; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http"), nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
		select {
		case <-accepted:
		case <-time.After(time.Second):
			t.Fatal("upgrade not registered")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	u.manager.Shutdown(ctx)
	elapsed := time.Since(started)
	t.Logf("130 unresponsive peers shut down in %v", elapsed)
	if elapsed > time.Second || u.manager.Count() != 0 {
		t.Fatalf("shutdown exceeded budget: %v, count=%d", elapsed, u.manager.Count())
	}
}

func TestEncodingFailureKeepsHealthyConnection(t *testing.T) {
	u, c, client := reviewPair(t)
	if err := c.Send(make(chan int)); err == nil {
		t.Fatal("expected encoding error")
	}
	if c.IsClosed() || u.manager.Count() != 1 {
		t.Fatal("encoding error closed healthy transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.SendText("ok"); err != nil {
		t.Fatal(err)
	}
	_, data, err := client.Read(ctx)
	if err != nil || string(data) != "ok" {
		t.Fatalf("healthy connection unusable: %s / %v", data, err)
	}
}

func TestReadAndWriteQueueTimeoutLeaveTransportUsable(t *testing.T) {
	_, c, client := reviewPair(t)
	c.readTimeout = 10 * time.Millisecond
	c.writeTimeout = 10 * time.Millisecond
	c.readGate <- struct{}{}
	c.writeGate <- struct{}{}
	_, readErr := c.ReadText()
	writeErr := c.SendText("must not send")
	<-c.readGate
	<-c.writeGate
	if !errors.Is(readErr, context.DeadlineExceeded) || !errors.Is(writeErr, context.DeadlineExceeded) || c.IsClosed() {
		t.Fatalf("queue errors: %v / %v, closed=%v", readErr, writeErr, c.IsClosed())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Write(ctx, coderws.MessageText, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if value, err := c.ReadText(); err != nil || value != "ok" {
		t.Fatalf("queue timeout damaged transport: %q %v", value, err)
	}
}

func TestConcurrentShutdownWaitsForSameCleanup(t *testing.T) {
	u, _, _ := reviewPair(t)
	first := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go func() { u.manager.Shutdown(ctx); close(first) }()
	deadline := time.Now().Add(time.Second)
	for !u.manager.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not start")
		}
		time.Sleep(time.Millisecond)
	}
	second := make(chan struct{})
	go func() { u.manager.Shutdown(context.Background()); close(second) }()
	select {
	case <-second:
		t.Fatal("concurrent shutdown returned during handshake")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first shutdown stuck")
	}
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("second shutdown stuck")
	}
}
