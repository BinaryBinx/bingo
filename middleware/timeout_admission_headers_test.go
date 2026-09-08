package middleware

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestTimeoutNativeAdmissionPreservesHeadersWithAndWithoutTracking(t *testing.T) {
	for _, mode := range []string{"standalone", "limiter", "tracker"} {
		t.Run(mode, func(t *testing.T) {
			release, finished := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			timed := Timeout(20 * time.Millisecond)(func(c *fasthttp.RequestCtx) {
				defer close(finished)
				calls.Add(1)
				<-release
				c.SetBodyString("late")
			})
			tracker := requestcontext.NewWorkTracker()
			if mode == "limiter" {
				timed = ConcurrencyLimit(2)(timed)
			}
			if mode == "tracker" {
				next := timed
				parent := requestcontext.WithWorkTracker(context.Background(), tracker)
				timed = func(c *fasthttp.RequestCtx) { requestcontext.Set(c, parent); next(c) }
			}
			h := CORS([]string{"https://client.test"}, nil, nil)(RequestID()(Security()(timed)))
			ln := fasthttputil.NewInmemoryListener()
			s := &fasthttp.Server{Concurrency: 1, Handler: h}
			done := make(chan error, 1)
			go func() { done <- s.Serve(ln) }()
			client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return ln.Dial() }}
			defer func() {
				close(release)
				client.CloseIdleConnections()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.ShutdownWithContext(ctx); err != nil {
					t.Error(err)
				}
				ln.Close()
				if err := <-done; err != nil {
					t.Error(err)
				}
				select {
				case <-finished:
				case <-ctx.Done():
					t.Error("worker did not exit")
				}
				select {
				case <-tracker.Stop():
				case <-ctx.Done():
					t.Error("rejected admission retained work")
				}
			}()
			var req fasthttp.Request
			var resp fasthttp.Response
			defer resp.Reset()
			req.SetRequestURI("http://test/occupy")
			req.Header.Set("Origin", "https://client.test")
			if err := client.DoTimeout(&req, &resp, time.Second); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode() != 408 {
				t.Fatalf("occupying response=%d", resp.StatusCode())
			}
			// The same keep-alive connection reaches native timeout admission
			// while the first worker still occupies the only native permit.
			if err := client.DoTimeout(&req, &resp, time.Second); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode() != 429 || calls.Load() != 1 {
				t.Fatalf("not a native rejection: status=%d calls=%d", resp.StatusCode(), calls.Load())
			}
			for _, name := range []string{"Access-Control-Allow-Origin", "X-Request-ID", "X-Content-Type-Options", "Content-Security-Policy"} {
				if len(resp.Header.Peek(name)) == 0 {
					t.Errorf("native 429 lost %s", name)
				}
			}
			if string(resp.Header.Peek("Cache-Control")) != "no-store" || string(resp.Header.Peek("Retry-After")) != "1" {
				t.Fatal("native rejection lost cache/retry policy")
			}
		})
	}
}
