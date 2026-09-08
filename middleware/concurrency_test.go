package middleware

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestConcurrencyLimitRetainsTimedOutWork(t *testing.T) {
	for _, order := range []string{"outside", "inside", "nested"} {
		t.Run(order, func(t *testing.T) {
			limiter := ConcurrencyLimit(2)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var entered atomic.Int32
			leaf := func(c *fasthttp.RequestCtx) {
				entered.Add(1)
				<-release // Deliberately ignore cancellation.
				c.SetBodyString("done")
			}
			h := limiter(Timeout(20 * time.Millisecond)(leaf))
			if order == "inside" {
				h = Timeout(20 * time.Millisecond)(limiter(leaf))
			}
			if order == "nested" {
				h = limiter(ConcurrencyLimit(3)(Timeout(20 * time.Millisecond)(ConcurrencyLimit(4)(leaf))))
			}
			request := middlewareServer(t, h)
			for i := 0; i < 2; i++ {
				if code := request().StatusCode(); code != 408 {
					t.Fatalf("status=%d, want timeout", code)
				}
			}
			for i := 0; i < 20; i++ {
				response := request()
				if response.StatusCode() != 503 || string(response.Header.Peek("Retry-After")) != "1" {
					t.Fatalf("overload not rejected: %d", response.StatusCode())
				}
			}
			if entered.Load() != 2 {
				t.Fatalf("timed-out handlers escaped limit: %d", entered.Load())
			}
			once.Do(func() { close(release) })
			// A separate handler wrapped by the same factory shares its budget.
			probe := limiter(func(c *fasthttp.RequestCtx) { c.SetBodyString("ready") })
			deadline := time.Now().Add(time.Second)
			for {
				var c fasthttp.RequestCtx
				probe(&c)
				ok := c.Response.StatusCode() == 200
				c.Response.Reset()
				if ok {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("completed work retained capacity")
				}
				time.Sleep(time.Millisecond)
			}
			if code := request().StatusCode(); code != 200 {
				t.Fatalf("limiter failed to recover: %d", code)
			}
		})
	}
}

func TestConcurrencyLimitReleasesNativeTimeoutRejection(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	limiter := ConcurrencyLimit(2)
	listener := fasthttputil.NewInmemoryListener()
	server := &fasthttp.Server{Concurrency: 1, Handler: limiter(Timeout(20 * time.Millisecond)(func(c *fasthttp.RequestCtx) {
		<-release
		c.SetBodyString("done")
	}))}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return listener.Dial() }}
	defer func() {
		client.CloseIdleConnections()
		listener.Close()
		if err := <-serveDone; err != nil {
			t.Error(err)
		}
	}()
	request := func() int {
		var req fasthttp.Request
		var resp fasthttp.Response
		defer resp.Reset()
		req.SetRequestURI("http://test/")
		// Reuse one connection so Server.Concurrency counts only the native
		// timeout worker, not another simultaneously admitted connection.
		if err := client.DoTimeout(&req, &resp, time.Second); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode()
	}
	if code := request(); code != 408 {
		t.Fatalf("first status=%d", code)
	}
	for i := 0; i < 5; i++ {
		if code := request(); code != 429 {
			t.Fatalf("native rejection retained limiter capacity: status=%d", code)
		}
	}
	once.Do(func() { close(release) })
	deadline := time.Now().Add(time.Second)
	for request() != 200 {
		if time.Now().After(deadline) {
			t.Fatal("admission did not recover")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConcurrencyLimitReleasesOnPanicAndDisabledTimeout(t *testing.T) {
	limiter := ConcurrencyLimit(1)
	panicHandler := Recovery()(limiter(Timeout(0)(func(*fasthttp.RequestCtx) { panic("business failure") })))
	for i := 0; i < 3; i++ {
		c := requestContext(t, "/", nil)
		panicHandler(c)
		if c.Response.StatusCode() != 500 || c.UserValue(concurrencyLeaseKey{}) != nil {
			t.Fatal("panic retained permit or user-value lease")
		}
	}
	for _, count := range []int{-1, 0} {
		c := requestContext(t, "/", nil)
		ConcurrencyLimit(count)(func(c *fasthttp.RequestCtx) { c.SetBodyString("disabled") })(c)
		if string(c.Response.Body()) != "disabled" {
			t.Fatal("disabled limiter rejected request")
		}
	}
}
