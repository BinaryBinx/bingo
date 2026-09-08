package middleware

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// A broken user closer can panic while fasthttp prepares a native 429 response,
// before its timeout worker is started. Subsequent closes succeed so Recovery
// and test cleanup can finish without masking the admission bookkeeping error.
type panicOnceAdmissionStream struct{ closes atomic.Int32 }

func (*panicOnceAdmissionStream) Read([]byte) (int, error) { return 0, io.EOF }
func (s *panicOnceAdmissionStream) Close() error {
	if s.closes.Add(1) == 1 {
		panic("admission response stream Close")
	}
	return nil
}

func TestTimeoutAdmissionPanicReleasesTrackedWorkAndConcurrency(t *testing.T) {
	tracker := requestcontext.NewWorkTracker()
	parent := requestcontext.WithWorkTracker(context.Background(), tracker)
	release := make(chan struct{})
	var releaseOnce sync.Once
	badStream := &panicOnceAdmissionStream{}
	limiter := ConcurrencyLimit(2)
	timed := limiter(Timeout(20 * time.Millisecond)(func(c *fasthttp.RequestCtx) {
		if string(c.Path()) == "/occupy" {
			<-release // Keep the native timeout semaphore occupied after the 408.
		}
		c.SetBodyString("done")
	}))
	handler := Recovery()(func(c *fasthttp.RequestCtx) {
		requestcontext.Set(c, parent)
		if string(c.Path()) == "/panic" {
			c.SetBodyStream(badStream, -1)
		}
		timed(c)
	})
	listener := fasthttputil.NewInmemoryListener()
	server := &fasthttp.Server{Concurrency: 1, Handler: handler}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return listener.Dial() }}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.ShutdownWithContext(ctx); err != nil {
			t.Error(err)
		}
		listener.Close()
		if err := <-serveDone; err != nil {
			t.Error(err)
		}
	}()
	request := func(path string) int {
		t.Helper()
		var req fasthttp.Request
		var resp fasthttp.Response
		defer resp.Reset()
		req.SetRequestURI("http://test" + path)
		// Reuse the same connection: Server.Concurrency must reject the timeout
		// worker, not reject a newly accepted connection before middleware runs.
		if err := client.DoTimeout(&req, &resp, time.Second); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode()
	}
	if status := request("/occupy"); status != fasthttp.StatusRequestTimeout {
		t.Fatalf("occupying request status=%d", status)
	}
	if status := request("/panic"); status != fasthttp.StatusInternalServerError {
		t.Fatalf("outer Recovery did not handle admission panic: status=%d", status)
	}
	if badStream.closes.Load() < 2 {
		t.Fatal("native rejection did not panic and retry closing its old stream")
	}
	// The original worker still holds one of two limiter permits. Native 429
	// proves the failed admission released the other; a leaked permit gives 503.
	if status := request("/probe"); status != fasthttp.StatusTooManyRequests {
		t.Errorf("admission panic leaked a concurrency permit: status=%d, want 429", status)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-tracker.Stop():
	case <-time.After(time.Second):
		t.Error("admission panic leaked a work reference and prevents shutdown draining")
	}
}
