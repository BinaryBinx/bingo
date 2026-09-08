package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/middleware"
	"github.com/valyala/fasthttp"
)

func servePhaseApp(t *testing.T, app *App) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.startServing(func() (net.Listener, error) { return listener, nil }); err != nil {
		t.Fatal(err)
	}
	return "http://" + listener.Addr().String() + "/"
}

func phaseRequest(t *testing.T, uri string) int {
	t.Helper()
	var req fasthttp.Request
	var resp fasthttp.Response
	defer resp.Reset()
	req.SetRequestURI(uri)
	req.Header.SetConnectionClose()
	if err := fasthttp.DoTimeout(&req, &resp, 2*time.Second); err != nil {
		t.Error(err)
		return 0
	}
	return resp.StatusCode()
}

func waitPhase(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown phase did not finish")
	}
}

func TestShutdownReleasesResourcesAfterInflightHTTP(t *testing.T) {
	app := reviewLatestApp(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var resourceClosed atomic.Bool
	app.GET("/", func(c *RequestContext) {
		close(entered)
		<-release
		if resourceClosed.Load() {
			c.String(500, "resource closed early")
			return
		}
		c.String(200, "done")
	})
	stopping := make(chan struct{})
	if err := app.OnStopping(func(context.Context) error { close(stopping); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := app.OnShutdown(func(context.Context) error { resourceClosed.Store(true); return nil }); err != nil {
		t.Fatal(err)
	}
	uri := servePhaseApp(t, app)
	response := make(chan int, 1)
	go func() { response <- phaseRequest(t, uri) }()
	waitPhase(t, entered)
	shutdown := make(chan error, 1)
	go func() { shutdown <- app.Shutdown() }()
	waitPhase(t, stopping)
	if resourceClosed.Load() {
		t.Fatal("cleanup ran before HTTP drain")
	}
	once.Do(func() { close(release) })
	if code := <-response; code != 200 {
		t.Fatalf("active request status=%d", code)
	}
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if !resourceClosed.Load() {
		t.Fatal("resource cleanup omitted")
	}
}

func TestShutdownTracksLateTimeoutWorkAndBudget(t *testing.T) {
	for _, exhaustBudget := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "deadline"}[exhaustBudget], func(t *testing.T) {
			app := reviewLatestApp(t)
			app.Use(middleware.Timeout(10 * time.Millisecond))
			app.Use(middleware.ConcurrencyLimit(1))
			release, finished := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			app.GET("/", func(c *RequestContext) { defer close(finished); <-release; c.String(200, "late") })
			stopping, cleanup := make(chan struct{}), make(chan struct{})
			app.OnStopping(func(context.Context) error { close(stopping); return nil })
			app.OnShutdown(func(context.Context) error {
				select {
				case <-finished:
				default:
					t.Error("resource cleanup overtook timeout worker")
				}
				close(cleanup)
				return nil
			})
			uri := servePhaseApp(t, app)
			if code := phaseRequest(t, uri); code != 408 {
				t.Fatalf("timeout status=%d", code)
			}
			budget := time.Second
			if exhaustBudget {
				budget = 40 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			shutdown := make(chan error, 1)
			go func() { shutdown <- app.ShutdownWithContext(ctx) }()
			waitPhase(t, stopping)
			select {
			case <-cleanup:
				t.Fatal("cleanup before work exit")
			default:
			}
			if exhaustBudget {
				select {
				case err := <-shutdown:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("deadline status=%v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("shutdown ignored deadline")
				}
				select {
				case <-cleanup:
					t.Fatal("deadline destroyed resources still in use")
				default:
				}
			} else {
				select {
				case err := <-shutdown:
					t.Fatalf("shutdown returned early: %v", err)
				default:
				}
			}
			once.Do(func() { close(release) })
			waitPhase(t, app.shutdownCh)
			waitPhase(t, cleanup)
			if !exhaustBudget {
				if err := <-shutdown; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type blockingPhaseResource struct{ entered, release chan struct{} }

func (r blockingPhaseResource) Close() error { close(r.entered); <-r.release; return nil }

func TestShutdownIncludesLateTimeoutResourceCleanup(t *testing.T) {
	app := reviewLatestApp(t)
	app.Use(middleware.Timeout(10 * time.Millisecond))
	workRelease, closeRelease, closeEntered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var workOnce, closeOnce sync.Once
	defer workOnce.Do(func() { close(workRelease) })
	defer closeOnce.Do(func() { close(closeRelease) })
	app.GET("/", func(c *RequestContext) {
		c.SetUserValue("resource", blockingPhaseResource{closeEntered, closeRelease})
		<-workRelease
	})
	cleaned := make(chan struct{})
	app.OnShutdown(func(context.Context) error { close(cleaned); return nil })
	if code := phaseRequest(t, servePhaseApp(t, app)); code != 408 {
		t.Fatalf("status=%d", code)
	}
	workOnce.Do(func() { close(workRelease) })
	waitPhase(t, closeEntered)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ignored pending cleanup: %v", err)
	}
	select {
	case <-cleaned:
		t.Fatal("application cleanup overtook request cleanup")
	default:
	}
	closeOnce.Do(func() { close(closeRelease) })
	waitPhase(t, app.shutdownCh)
	waitPhase(t, cleaned)
}

func TestAppTimeoutAdmissionRejectionDoesNotRetainWork(t *testing.T) {
	app := reviewLatestApp(t)
	app.server.Concurrency = 1
	app.Use(middleware.Timeout(10 * time.Millisecond))
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	app.GET("/", func(c *RequestContext) { <-release; c.String(200, "done") })
	uri := servePhaseApp(t, app)
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for i := 0; i < 6; i++ {
		resp, err := client.Get(uri)
		if err != nil {
			t.Fatal(err)
		}
		// Consume the body to retain the same connection and its native worker.
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		want := 429
		if i == 0 {
			want = 408
		}
		if resp.StatusCode != want {
			t.Fatalf("request %d: %d, want %d", i, resp.StatusCode, want)
		}
	}
	once.Do(func() { close(release) })
	transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		t.Fatal("native rejection leaked a work reference: ", err)
	}
}

func TestAppPublishesSharedCancellationParent(t *testing.T) {
	app := reviewLatestApp(t)
	var parent context.Context
	app.GET("/", func(c *RequestContext) { parent = c.Context() })
	var raw fasthttp.RequestCtx
	raw.Request.SetRequestURI("/")
	app.handleRequest(&raw)
	defer raw.Response.Reset()
	if parent != app.ctx {
		t.Fatal("request did not receive the shared application parent")
	}
	const count = 128
	before := runtime.NumGoroutine()
	cancels := make([]context.CancelFunc, count)
	for i := range cancels {
		_, cancels[i] = context.WithTimeout(parent, time.Minute)
	}
	after := runtime.NumGoroutine()
	for _, cancel := range cancels {
		cancel()
	}
	if added := after - before; added > 8 {
		t.Fatalf("deadline creation spawned cancellation bridges: %d", added)
	}
	if err := app.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if parent.Err() != context.Canceled {
		t.Fatal("application shutdown did not cancel business parent")
	}
	raw.Response.Reset()
	app.handleRequest(&raw)
	if raw.Response.StatusCode() != 503 {
		t.Fatal("request admitted after shutdown")
	}
}

func TestRunReturnsFirstShutdownDeadlineWhileLateWorkDrains(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	app := reviewLatestApp(t)
	app.config.Host, app.config.Port = "127.0.0.1", port
	app.Use(middleware.Timeout(10 * time.Millisecond))
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	app.GET("/slow", func(c *RequestContext) { <-release; c.String(200, "late") })
	run := make(chan error, 1)
	go func() { run <- app.Run() }()
	address := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: time.Second}
	for until := time.Now().Add(time.Second); ; {
		resp, err := client.Get(address + "/ready")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(until) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if code := phaseRequest(t, address+"/slow"); code != 408 {
		t.Fatalf("status=%d", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown=%v", err)
	}
	select {
	case err := <-run:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run concealed deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run waited indefinitely after shutdown deadline")
	}
	once.Do(func() { close(release) })
	waitPhase(t, app.shutdownCh)
}
