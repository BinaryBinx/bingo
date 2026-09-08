package middleware

import (
	"context"
	"fmt"
	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func middlewareServer(t *testing.T, handler fasthttp.RequestHandler) func() *fasthttp.Response {
	t.Helper()
	listener := fasthttputil.NewInmemoryListener()
	server := &fasthttp.Server{Handler: handler}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return listener.Dial() }}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		listener.Close()
		if err := <-serveDone; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return func() *fasthttp.Response {
		t.Helper()
		var request fasthttp.Request
		request.SetRequestURI("http://test/")
		request.Header.Set("Accept-Encoding", "gzip")
		request.Header.SetConnectionClose()
		response := &fasthttp.Response{}
		if err := client.DoTimeout(&request, response, 2*time.Second); err != nil {
			t.Fatalf("request: %v", err)
		}
		t.Cleanup(response.Reset)
		return response
	}
}

func TestTimeoutMiddlewareOrdersAreRaceFree(t *testing.T) {
	for _, name := range []string{"cache-outside", "cache-inside", "compress-outside", "compress-inside", "logger-outside", "logger-inside", "recovery-outside", "recovery-inside"} {
		t.Run(name, func(t *testing.T) {
			finished := make(chan struct{})
			leaf := func(ctx *fasthttp.RequestCtx) {
				defer close(finished)
				until := time.Now().Add(40 * time.Millisecond)
				payload := strings.Repeat("payload", 100)
				// Deliberately ignore cancellation briefly to exercise the worker
				// lifetime that caused the original Response.Body data race.
				for time.Now().Before(until) {
					ctx.SetBodyString(payload)
				}
			}
			var middleware func(fasthttp.RequestHandler) fasthttp.RequestHandler
			switch strings.Split(name, "-")[0] {
			case "cache":
				middleware = Cache(time.Minute)
			case "compress":
				middleware = Compress()
			case "logger":
				middleware = LoggerWithConfig(LoggerConfig{Printf: func(string, ...any) {}})
			case "recovery":
				middleware = Recovery()
			}
			handler := middleware(Timeout(5 * time.Millisecond)(leaf))
			if strings.HasSuffix(name, "inside") {
				handler = Timeout(5 * time.Millisecond)(middleware(leaf))
			}
			response := middlewareServer(t, handler)()
			<-finished
			if response.StatusCode() != 408 {
				t.Fatalf("status=%d", response.StatusCode())
			}
		})
	}
}

func TestTimeoutNeverCachesPartialResponse(t *testing.T) {
	for _, outside := range []bool{true, false} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			var calls atomic.Int32
			release, finished := make(chan struct{}), make(chan struct{})
			leaf := func(ctx *fasthttp.RequestCtx) {
				if calls.Add(1) == 1 {
					defer close(finished)
					ctx.SetBodyString("partial")
					<-release
				}
				ctx.SetBodyString("complete")
			}
			handler := Cache(time.Minute)(Timeout(5 * time.Millisecond)(leaf))
			if !outside {
				handler = Timeout(5 * time.Millisecond)(Cache(time.Minute)(leaf))
			}
			request := middlewareServer(t, handler)
			first := request()
			close(release)
			<-finished
			second := request()
			if first.StatusCode() != 408 || second.StatusCode() != 200 || string(second.Body()) != "complete" {
				t.Fatalf("first=%d second=%d %q", first.StatusCode(), second.StatusCode(), second.Body())
			}
			if calls.Load() != 2 {
				t.Fatalf("partial result cached, calls=%d", calls.Load())
			}
		})
	}
}

func TestTimeoutRecoversMiddlewarePanic(t *testing.T) {
	for _, afterDeadline := range []bool{false, true} {
		t.Run(fmt.Sprint(afterDeadline), func(t *testing.T) {
			finished := make(chan struct{})
			leaf := func(ctx *fasthttp.RequestCtx) {
				defer close(finished)
				if afterDeadline {
					time.Sleep(15 * time.Millisecond)
				}
				ctx.Response.Header.Set("Content-Encoding", "gzip")
				panic("middleware failure")
			}
			response := middlewareServer(t, Recovery()(Timeout(5*time.Millisecond)(leaf)))()
			<-finished
			want := 500
			if afterDeadline {
				want = 408
			}
			if response.StatusCode() != want || len(response.Header.Peek("Content-Encoding")) != 0 {
				t.Fatalf("status=%d encoding=%q", response.StatusCode(), response.Header.Peek("Content-Encoding"))
			}
		})
	}
}

func TestTimeoutCancellationAndNestedOwnership(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		finished := make(chan error, 1)
		handler := Timeout(5 * time.Millisecond)(func(ctx *fasthttp.RequestCtx) {
			work := requestcontext.From(ctx)
			<-work.Done()
			finished <- work.Err()
		})
		response := middlewareServer(t, handler)()
		if err := <-finished; err != context.DeadlineExceeded {
			t.Fatalf("cancel reason=%v", err)
		}
		if response.StatusCode() != 408 {
			t.Fatalf("status=%d", response.StatusCode())
		}
	})
	t.Run("nested", func(t *testing.T) {
		handler := Timeout(time.Second)(Timeout(time.Nanosecond)(func(ctx *fasthttp.RequestCtx) { time.Sleep(time.Millisecond); ctx.SetBodyString("done") }))
		response := middlewareServer(t, handler)()
		if response.StatusCode() != 200 || string(response.Body()) != "done" {
			t.Fatalf("nested timeout created a second worker: %d", response.StatusCode())
		}
	})
}

func TestTimeoutPreservesSuccessfulHijack(t *testing.T) {
	handler := Timeout(time.Second)(func(ctx *fasthttp.RequestCtx) {
		ctx.HijackSetNoResponse(true)
		ctx.Hijack(func(conn net.Conn) {
			fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		})
	})
	response := middlewareServer(t, handler)()
	if response.StatusCode() != 200 || string(response.Body()) != "ok" {
		t.Fatal("successful hijack lost the real connection")
	}
}

type timeoutResource struct{ closed chan struct{} }

func (resource *timeoutResource) Read([]byte) (int, error) { return 0, io.EOF }
func (resource *timeoutResource) Close() error             { close(resource.closed); return nil }

func TestTimeoutClosesLateStreamAndUserResources(t *testing.T) {
	release := make(chan struct{})
	stream := &timeoutResource{closed: make(chan struct{})}
	userResource := &timeoutResource{closed: make(chan struct{})}
	handler := Timeout(5 * time.Millisecond)(func(ctx *fasthttp.RequestCtx) {
		<-release
		ctx.SetBodyStream(stream, -1)
		ctx.SetUserValue("bad-resource", panickingTimeoutResource{})
		ctx.SetUserValue("resource", userResource)
	})
	response := middlewareServer(t, handler)()
	close(release)
	if response.StatusCode() != 408 {
		t.Fatalf("status=%d", response.StatusCode())
	}
	for _, resource := range []*timeoutResource{stream, userResource} {
		select {
		case <-resource.closed:
		case <-time.After(time.Second):
			t.Fatal("timed out request retained an open resource")
		}
	}
}

type panickingTimeoutResource struct{}

func (panickingTimeoutResource) Close() error { panic("bad resource Close") }
