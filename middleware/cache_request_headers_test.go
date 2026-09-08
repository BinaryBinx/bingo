package middleware

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCacheHeaderSnapshotOwnsOriginalFields(t *testing.T) {
	for _, normalize := range []bool{true, false} {
		t.Run(fmt.Sprint(normalize), func(t *testing.T) {
			var h fasthttp.RequestHeader
			if !normalize {
				h.DisableNormalizing()
			}
			h.SetHost("example.test")
			h.SetUserAgent("snapshot-test")
			h.Add("x-VARIANT", "")
			h.Add("X-Variant", "second;7:value")
			h.Add("X-Empty", "")
			fields := []string{"host", "user-agent", "x-empty", "x-missing", "x-variant"}
			want := cacheVariant(fields, &h)
			pool := make(cacheHeaderPool, 1)
			snapshot := pool.get()
			snapshot.copyFrom(&h)
			h.Reset()
			h.Set("X-Variant", "rewritten")
			got := snapshot.variant(fields)
			if got != want {
				t.Fatalf("original fields changed: %q != %q", got, want)
			}
			pool.put(snapshot)
			reused := pool.get()
			reused.copyFrom(&h)
			if reused.variant(fields) != cacheVariant(fields, &h) || got != want {
				t.Fatal("snapshot reuse changed an owned variant key or kept stale fields")
			}
			pool.put(reused)
		})
	}
}

func TestCacheHeaderPoolBoundsIdleMemory(t *testing.T) {
	for _, kind := range []string{"large-value", "many-fields"} {
		t.Run(kind, func(t *testing.T) {
			pool := make(cacheHeaderPool, 2)
			var h fasthttp.RequestHeader
			if kind == "large-value" {
				h.Set("X-Large", strings.Repeat("x", cacheHeaderPoolBytes+1))
			} else {
				for i := 0; i <= cacheHeaderPoolFields; i++ {
					h.Add(fmt.Sprintf("X-%d", i), "value")
				}
			}
			snapshot := pool.get()
			snapshot.copyFrom(&h)
			// Oversized input must still produce the right Vary key for this request.
			fields := []string{"x-large", "x-0"}
			if snapshot.variant(fields) != cacheVariant(fields, &h) {
				t.Fatal("large request lost header values")
			}
			pool.put(snapshot)
			if len(pool) != 0 {
				t.Fatal("oversized backing storage was retained for reuse")
			}
		})
	}
	pool := make(cacheHeaderPool, 2)
	a, b, c := pool.get(), pool.get(), pool.get()
	var h fasthttp.RequestHeader
	h.Set("X-Variant", "old")
	for _, snapshot := range []*cacheRequestHeaders{a, b, c} {
		snapshot.copyFrom(&h)
		pool.put(snapshot)
	}
	if len(pool) != cap(pool) {
		t.Fatal("idle snapshot count is not bounded")
	}
	for range 2 {
		snapshot := pool.get()
		if len(snapshot.fields) != 0 || len(snapshot.data) != 0 {
			t.Fatal("pooled snapshot still exposes the previous request")
		}
	}
}

func TestCachePooledHeadersPreserveVaryAfterRequestRewrite(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute, MaxEntries: 1024})
	defer cache.Close()
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		body := fmt.Sprintf("%q", c.Request.Header.PeekAll("X-Variant"))
		c.Request.Header.Set("X-Variant", "rewritten by handler")
		c.Response.Header.Set("Vary", "X-Variant")
		c.SetBodyString(body)
	})
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Go(func() {
			for n := 0; n < 48; n++ {
				var c fasthttp.RequestCtx
				c.Request.SetRequestURI("/variants")
				c.Request.Header.Add("X-Variant", "")
				c.Request.Header.Add("X-Variant", fmt.Sprintf("worker-%d/variant-%d", worker, n%3))
				want := fmt.Sprintf("%q", c.Request.Header.PeekAll("X-Variant"))
				h(&c)
				if string(c.Response.Body()) != want {
					t.Errorf("cross-request header data: %q != %q", c.Response.Body(), want)
				}
				if n >= 3 && string(c.Response.Header.Peek("X-Cache")) != "HIT" {
					t.Error("warm variant became unreachable during another schema publication")
				}
				c.Response.Reset()
			}
		})
	}
	workers.Wait()
	// Every representation remains reachable using its original request fields
	// after all request-header snapshots have been returned to the pool.
	for worker := 0; worker < 16; worker++ {
		for variant := 0; variant < 3; variant++ {
			c := requestContext(t, "/variants", nil)
			c.Request.Header.Add("X-Variant", "")
			c.Request.Header.Add("X-Variant", fmt.Sprintf("worker-%d/variant-%d", worker, variant))
			want := fmt.Sprintf("%q", c.Request.Header.PeekAll("X-Variant"))
			h(c)
			if string(c.Response.Body()) != want || string(c.Response.Header.Peek("X-Cache")) != "HIT" {
				t.Fatal("rewritten request headers changed the stored Vary key")
			}
		}
	}
}
