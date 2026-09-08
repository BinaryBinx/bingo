package core

import (
	"github.com/BinaryBinx/bingo/middleware"

	"github.com/valyala/fasthttp"
	"net"
	"net/http"

	"strconv"
	"strings"
	"testing"
	"time"
)

func reviewLatestApp(t *testing.T) *App {
	cfg := DefaultConfig()
	cfg.RunMode = RunModeTest
	cfg.MultiCore.Enabled = false
	app := NewApp(cfg)
	t.Cleanup(func() { app.Shutdown() })
	return app
}

func TestReviewRunWaitsForInflightHTTP(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	app := reviewLatestApp(t)
	app.config.Host = "127.0.0.1"
	app.config.Port = port
	entered, release := make(chan struct{}), make(chan struct{})
	app.GET("/slow", func(ctx *RequestContext) { close(entered); <-release; ctx.String(200, "complete") })
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	address := "http://127.0.0.1:" + strconv.Itoa(port)
	for until := time.Now().Add(time.Second); ; {
		resp, e := http.Get(address + "/ready")
		if e == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(until) {
			t.Fatal(e)
		}
		time.Sleep(time.Millisecond)
	}
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, e := http.Get(address + "/slow")
		if e == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown() }()
	select {
	case err := <-runDone:
		t.Errorf("Run returned %v while handler is blocked and Shutdown has not finished", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	<-clientDone
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}

func TestReviewTimeoutDocumentedOrder(t *testing.T) {
	app := reviewLatestApp(t)
	app.Use(middleware.Timeout(5 * time.Millisecond)) // First registered is outermost.
	app.Use(middleware.Recovery())
	app.Use(middleware.Cache(time.Minute))
	app.Use(middleware.Compress())
	finished := make(chan struct{})
	app.GET("/", func(ctx *RequestContext) {
		defer close(finished)
		payload := strings.Repeat("payload", 300)
		until := time.Now().Add(60 * time.Millisecond)
		for time.Now().Before(until) {
			ctx.SetBodyString(payload)
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Serve(ln) }()
	var req fasthttp.Request
	var resp fasthttp.Response
	req.SetRequestURI("http://" + ln.Addr().String() + "/")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.SetConnectionClose()
	if err := fasthttp.DoTimeout(&req, &resp, time.Second); err != nil {
		t.Error(err)
	}
	<-finished
	app.Shutdown()
	<-done
	t.Logf("response status=%d", resp.StatusCode())
	if resp.StatusCode() != fasthttp.StatusRequestTimeout {
		t.Fatalf("expected 408, got %d", resp.StatusCode())
	}
}
