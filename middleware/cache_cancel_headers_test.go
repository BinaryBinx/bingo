package middleware

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
)

func TestCacheCanceledWaiterPreservesPublicHeaders(t *testing.T) {
	for _, kind := range []string{"canceled", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				c.SetBodyString("origin completed")
			})
			leader := requestContext(t, "/wait", nil)
			go func() { defer close(done); h(leader) }()
			<-entered
			defer func() { close(release); <-done }()
			ctx, cancel := context.WithCancel(context.Background())
			if kind == "deadline" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			c := requestContext(t, "/wait", nil)
			requestcontext.Set(c, ctx)
			public := map[string]string{
				"Access-Control-Allow-Origin": "https://frontend.test",
				"X-Request-ID":                "waiter-id",
				"X-Content-Type-Options":      "nosniff",
				"Content-Security-Policy":     "default-src 'self'",
			}
			for name, value := range public {
				c.Response.Header.Set(name, value)
			}
			c.Response.Header.Set("Content-Encoding", "gzip")
			c.Response.Header.Set("Content-Digest", "old-digest")
			c.Response.Header.Set("ETag", `"old"`)
			if err := c.Response.Header.SetTrailer("X-Old-Trailer"); err != nil {
				t.Fatal(err)
			}
			c.Response.Header.Set("X-Old-Trailer", "old")
			c.SetBodyString("old body")
			h(c)
			if c.Response.StatusCode() != 408 || string(c.Response.Body()) != "Request timeout" || calls.Load() != 1 {
				t.Fatal("waiter did not cancel independently of the active origin")
			}
			for name, want := range public {
				if string(c.Response.Header.Peek(name)) != want {
					t.Errorf("lost public header %s", name)
				}
			}
			for _, name := range []string{"Content-Encoding", "Content-Digest", "ETag", "Trailer", "X-Old-Trailer"} {
				if len(c.Response.Header.Peek(name)) != 0 {
					t.Errorf("retained old representation header %s", name)
				}
			}
			if string(c.Response.Header.Peek("Cache-Control")) != "no-store" {
				t.Fatal("cancellation response permits caching")
			}
		})
	}
}
