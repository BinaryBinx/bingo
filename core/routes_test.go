package core

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestRoutesFreezeWithoutLockingRequests(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RunMode = RunModeTest
	app := NewApp(cfg)
	defer app.Shutdown()
	leaf := func(c *RequestContext) { c.String(200, "ok") }
	app.GET("/seed", leaf)
	group := app.Group("/api").Group("/v1")
	if err := group.Handle("GET", "/before", leaf); err != nil {
		t.Fatal(err)
	}
	if err := app.startServing(func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			var c fasthttp.RequestCtx
			c.Request.SetRequestURI("/seed")
			for range 1000 {
				c.Response.Reset()
				app.handleRequest(&c)
				if c.Response.StatusCode() != 200 {
					t.Error("registered route changed")
				}
			}
		})
	}
	for i := range 1000 {
		if err := group.Handle("GET", fmt.Sprint("/late/", i), leaf); !errors.Is(err, ErrRoutesFrozen) {
			t.Fatalf("late registration: %v", err)
		}
	}
	wg.Wait()
	for i, register := range []func(string, RequestHandler){
		app.GET, app.POST, app.PUT, app.DELETE, app.PATCH, app.HEAD, app.OPTIONS,
		group.GET, group.POST, group.PUT, group.DELETE, group.PATCH, group.HEAD, group.OPTIONS,
	} {
		func() {
			defer func() {
				err, _ := recover().(error)
				if !errors.Is(err, ErrRoutesFrozen) {
					t.Errorf("registration %d panic=%v", i, err)
				}
			}()
			register("/late", leaf)
		}()
	}
}

func TestRoutesConcurrentSetupAndShutdownSeal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RunMode = RunModeTest
	app := NewApp(cfg)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			if err := app.Handle("GET", fmt.Sprint("/setup/", i), func(*RequestContext) {}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := app.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := app.Handle("GET", "/after", func(*RequestContext) {}); !errors.Is(err, ErrRoutesFrozen) {
		t.Fatal(err)
	}
}
