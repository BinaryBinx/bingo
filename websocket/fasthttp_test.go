package websocket

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/valyala/fasthttp"
)

func testFastHTTPServer(t *testing.T, u *WebSocketUpgrader, session func(*Connection), mutate bool) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.Set("X-Handshake", "preserved")
		if err := u.UpgradeFastHTTP(ctx, session); err != nil {
			return
		}
		if mutate {
			// The callback must only use the independent request copy.
			ctx.Request.Header.Set("Sec-WebSocket-Key", "changed after scheduling")
			ctx.Request.Header.Set("Origin", "https://changed.invalid")
		}
	}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = u.manager.ShutdownWithContext(ctx)
		_ = server.ShutdownWithContext(ctx)
		if err := <-done; err != nil {
			t.Errorf("server.Serve: %v", err)
		}
	})
	return listener.Addr().String()
}

func TestFastHTTPHandshakeEchoCompressionAndRequestCopy(t *testing.T) {
	u := testUpgrader(t)
	completed := make(chan struct{})
	address := testFastHTTPServer(t, u, func(conn *Connection) {
		defer close(completed)
		body, err := conn.ReadText()
		if err == nil {
			_ = conn.SendText(body)
		}
	}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, response, err := coderws.Dial(ctx, "ws://"+address+"/ws", &coderws.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://" + address}}, CompressionMode: coderws.CompressionNoContextTakeover,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	if response.Header.Get("X-Handshake") != "preserved" || !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		t.Fatalf("handshake headers: %v", response.Header)
	}
	want := strings.Repeat("WebSocket 中文", 100)
	if err := client.Write(ctx, coderws.MessageText, []byte(want)); err != nil {
		t.Fatal(err)
	}
	_, got, err := client.Read(ctx)
	if err != nil || string(got) != want {
		t.Fatalf("echo err=%v bytes=%d", err, len(got))
	}
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("session did not finish")
	}
}

func TestFastHTTPRejectsCrossOriginAndShutdown(t *testing.T) {
	u := testUpgrader(t)
	address := testFastHTTPServer(t, u, func(*Connection) { t.Error("invalid handshake reached handler") }, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, response, err := coderws.Dial(ctx, "ws://"+address+"/ws", &coderws.DialOptions{HTTPHeader: http.Header{"Origin": {"https://untrusted.invalid"}}})
	if client != nil {
		client.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin rejection response=%v err=%v", response, err)
	}
	u.manager.Shutdown(context.Background())
	client, response, err = coderws.Dial(ctx, "ws://"+address+"/ws", nil)
	if client != nil {
		client.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("shutdown rejection response=%v err=%v", response, err)
	}
}

func TestFastHTTPPreservesFrameBufferedWithHandshake(t *testing.T) {
	u := testUpgrader(t)
	address := testFastHTTPServer(t, u, func(conn *Connection) {
		text, err := conn.ReadText()
		if err == nil {
			_ = conn.SendText(text)
		}
	}, false)
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(2 * time.Second))
	handshake := fmt.Sprintf("GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", address)
	frame := []byte{0x81, 0x85, 1, 2, 3, 4}
	for i, b := range []byte("hello") {
		frame = append(frame, b^byte(i%4+1))
	}
	if _, err := raw.Write(append([]byte(handshake), frame...)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(raw)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("response=%v err=%v", response, err)
	}
	got := make([]byte, 7)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if got[0] != 0x81 || got[1] != 5 || string(got[2:]) != "hello" {
		t.Fatalf("buffered frame lost: %v", got)
	}
}

func TestShutdownInterruptsPendingHandshake(t *testing.T) {
	u := testUpgrader(t)
	reservation, err := u.manager.reserve()
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer client.Close()
	request, _ := http.NewRequest("GET", "http://example.test/ws", nil)
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	finished := make(chan error, 1)
	go func() {
		defer reservation.release()
		_, err := u.accept(server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), make(http.Header), request, reservation)
		finished <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := u.manager.ShutdownWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err == nil {
		t.Fatal("shutdown let an in-flight handshake succeed")
	}
	if u.manager.Count() != 0 {
		t.Fatal("in-flight upgrade escaped shutdown")
	}
}

// The default fasthttp hijack wrapper ignores Close; shutdown must reach the
// actual transport without requiring KeepHijackedConns to be enabled.
func TestFastHTTPShutdownUnblocksSessionRead(t *testing.T) {
	u := testUpgrader(t)
	started, finished := make(chan struct{}), make(chan struct{})
	address := testFastHTTPServer(t, u, func(conn *Connection) {
		close(started)
		defer close(finished)
		_, _ = conn.ReadText()
	}, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws://"+address+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("session did not start")
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	_ = u.manager.ShutdownWithContext(shutdownCtx)
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("shutdown retained a hijack reader")
	}
	if u.manager.Count() != 0 {
		t.Fatal("closed connection remained registered")
	}
}
