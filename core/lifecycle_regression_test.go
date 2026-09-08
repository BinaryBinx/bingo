package core

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestShutdownBeforeServeRejectsStartup(t *testing.T) {
	app := NewApp(nil)
	if err := app.Shutdown(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.Serve(ln); !errors.Is(err, ErrAppClosed) {
		t.Fatal(err)
	}
	if _, err = ln.Accept(); err == nil {
		t.Fatal("rejected listener left open")
	}
	if app.GetWebSocketUpgrader().GetManager().Count() != 0 {
		t.Fatal("closed upgrader state")
	}
}

func TestDuplicateServeDoesNotCloseRunningServer(t *testing.T) {
	app := NewApp(nil)
	defer app.Shutdown()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.startServing(func() (net.Listener, error) { return ln, nil }); err != nil {
		t.Fatal(err)
	}
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.Serve(other); !errors.Is(err, fasthttp.ErrAlreadyServing) {
		t.Fatal(err)
	}
	var req fasthttp.Request
	var resp fasthttp.Response
	req.SetRequestURI("http://" + ln.Addr().String() + "/")
	req.Header.SetConnectionClose()
	if err = fasthttp.DoTimeout(&req, &resp, time.Second); err != nil {
		t.Fatal("duplicate start damaged live server: ", err)
	}
}

func TestConcurrentAppShutdownWaitsAndPropagatesHookError(t *testing.T) {
	app := NewApp(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	want := errors.New("cleanup failure")
	if err := app.OnShutdown(func(context.Context) error { close(entered); <-release; return want }); err != nil {
		t.Fatal(err)
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- app.Shutdown() }()
	<-entered
	go func() { second <- app.Shutdown() }()
	select {
	case <-second:
		t.Error("second shutdown returned before cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for _, result := range []<-chan error{first, second} {
		select {
		case err := <-result:
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown stuck")
		}
	}
}
