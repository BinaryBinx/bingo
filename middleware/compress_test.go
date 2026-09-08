package middleware

import (
	"fmt"
	"github.com/valyala/fasthttp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompressNegotiationAndVary(t *testing.T) {
	for _, item := range []struct {
		accept string
		gzip   bool
	}{
		{"gzip", true}, {"gzip;q=0", false}, {"notgzip", false}, {"br, gzip;q=0.5", true}, {"*", true}, {"*;q=1,gzip;q=0", false}, {"gzip;q=NaN", false}, {"gzip;q=2", false}, {"", false},
	} {
		t.Run(item.accept, func(t *testing.T) {
			handler := Compress()(func(ctx *fasthttp.RequestCtx) {
				ctx.Response.Header.Set("Vary", "Origin")
				ctx.SetBodyString(strings.Repeat("payload", 100))
			})
			ctx := requestContext(t, "/", map[string]string{"Accept-Encoding": item.accept})
			handler(ctx)
			if got := string(ctx.Response.Header.Peek("Content-Encoding")) == "gzip"; got != item.gzip {
				t.Fatalf("gzip=%v", got)
			}
			vary := joinHeaderValues(ctx.Response.Header.PeekAll("Vary"))
			if !strings.Contains(vary, "Origin") || !strings.Contains(vary, "Accept-Encoding") {
				t.Fatalf("Vary lost: %s", vary)
			}
			if item.gzip {
				body, err := ctx.Response.BodyGunzip()
				if err != nil || string(body) != strings.Repeat("payload", 100) {
					t.Fatalf("invalid compressed response: %v", err)
				}
			}
		})
	}
}

func TestCacheCompressBothOrders(t *testing.T) {
	for _, outerCache := range []bool{true, false} {
		t.Run(fmt.Sprint(outerCache), func(t *testing.T) {
			leaf := func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString(strings.Repeat("payload", 100)) }
			handler := Cache(time.Minute)(Compress()(leaf))
			if !outerCache {
				handler = Compress()(Cache(time.Minute)(leaf))
			}
			for _, accept := range []string{"gzip", "", "gzip", ""} {
				ctx := requestContext(t, "/", map[string]string{"Accept-Encoding": accept})
				handler(ctx)
				if got := string(ctx.Response.Header.Peek("Content-Encoding")); (got == "gzip") != (accept == "gzip") {
					t.Fatalf("accept=%q encoding=%q", accept, got)
				}
			}
		})
	}
}

func TestRateBucketIdleAndConcurrentBurst(t *testing.T) {
	now := time.Now()
	bucket := &tokenBucket{rate: 1, capacity: 2, tokens: 2, last: now}
	for i := 0; i < 2; i++ {
		if !bucket.allow(now.Add(10 * time.Second)) {
			t.Fatal("missing token")
		}
	}
	if bucket.allow(now.Add(10 * time.Second)) {
		t.Fatal("idle time accumulated beyond burst")
	}
	if !bucket.allow(now.Add(11 * time.Second)) {
		t.Fatal("new second did not refill")
	}
	concurrentBucket := &tokenBucket{rate: 1, capacity: 10, tokens: 10, last: now}
	var accepted atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 100; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if concurrentBucket.allow(now) {
				accepted.Add(1)
			}
		}()
	}
	workers.Wait()
	if accepted.Load() != 10 {
		t.Fatalf("concurrent burst accepted %d", accepted.Load())
	}
	handler := RateLimit(int(^uint(0) >> 1))(func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("ok") })
	ctx := requestContext(t, "/", nil)
	handler(ctx)
	if ctx.Response.StatusCode() != 200 {
		t.Fatal("large rate rejected")
	}
}

func TestCORSOrdinaryOptionsAndExistingVary(t *testing.T) {
	called := false
	handler := CORS([]string{"https://example.com"}, nil, nil)(func(ctx *fasthttp.RequestCtx) { called = true; ctx.SetBodyString("options") })
	ctx := requestContext(t, "/", nil)
	ctx.Request.Header.SetMethod("OPTIONS")
	ctx.Response.Header.Set("Vary", "Accept-Encoding")
	handler(ctx)
	if !called || ctx.Response.StatusCode() != 200 {
		t.Fatal("ordinary OPTIONS intercepted")
	}
	values := joinHeaderValues(ctx.Response.Header.PeekAll("Vary"))
	if !strings.Contains(values, "Accept-Encoding") {
		t.Fatal("existing Vary overwritten")
	}
}

func TestRecoveryClearsPartialRepresentation(t *testing.T) {
	handler := Recovery()(func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.Set("Content-Encoding", "gzip")
		ctx.Response.Header.Set("Location", "/elsewhere")
		ctx.Response.Header.Set("Content-Length", "999")
		panic("partial response")
	})
	ctx := requestContext(t, "/", nil)
	handler(ctx)
	if ctx.Response.StatusCode() != 500 || string(ctx.Response.Body()) != "Internal Server Error" {
		t.Fatal("invalid error")
	}
	for _, name := range []string{"Content-Encoding", "Location"} {
		if len(ctx.Response.Header.Peek(name)) != 0 {
			t.Fatalf("stale %s", name)
		}
	}
}
