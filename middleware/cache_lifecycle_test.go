package middleware

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func assertCacheIndex(t *testing.T, c *CacheHandler) {
	t.Helper()
	cache := c.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	count := 0
	for group, head := range cache.groups {
		var previous *cacheEntry[responseCacheKey, responseSnapshot]
		for e := head; e != nil; e = e.groupNext {
			count++
			if count > len(cache.items) {
				t.Fatal("index cycle or orphan")
			}
			if cache.items[e.key] != e || cache.groupOf(e.key) != group || e.groupPrevious != previous {
				t.Fatal("invalid index membership")
			}
			previous = e
		}
	}
	if count != len(cache.items) || len(cache.groups) > len(cache.items) {
		t.Fatalf("index=%d entries=%d groups=%d", count, len(cache.items), len(cache.groups))
	}
	if cache.bytes > cache.maxBytes || len(cache.items) > cache.maxEntries {
		t.Fatal("capacity exceeded")
	}
}

func TestCacheIndexFollowsReplacementEvictionAndExpiry(t *testing.T) {
	c := NewCacheHandler(CacheConfig{Duration: time.Hour, MaxEntries: 12, MaxBytes: 8192})
	defer c.Close()
	for i := range 500 {
		key := responseCacheKey{host: "test", uri: fmt.Sprint("/", i%20), variant: fmt.Sprint(i % 3)}
		c.cache.put(key, responseSnapshot{body: []byte("data")}, 512, time.Now().Add(time.Hour))
		assertCacheIndex(t, c)
		if i%4 == 0 {
			if err := c.Invalidate("test", key.uri); err != nil {
				t.Fatal(err)
			}
			assertCacheIndex(t, c)
		}
	}
	c.Clear()
	c.cache.put(responseCacheKey{host: "test", uri: "/expiring"}, responseSnapshot{}, 512, time.Now().Add(10*time.Millisecond))
	for until := time.Now().Add(time.Second); ; {
		c.cache.mu.Lock()
		empty := len(c.cache.items) == 0
		c.cache.mu.Unlock()
		if empty {
			break
		}
		if time.Now().After(until) {
			t.Fatal("expiry did not remove entry")
		}
		time.Sleep(time.Millisecond)
	}
	assertCacheIndex(t, c)
}

func TestCacheClearAndCloseFenceOldWorkAndWakeWaiters(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprint(closing), func(t *testing.T) {
			c := NewCacheHandler(CacheConfig{Duration: time.Hour, CoalesceHeaders: []string{}})
			defer c.Close()
			entered, release, oldDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer func() { once.Do(func() { close(release) }); <-oldDone }()
			var calls atomic.Int32
			h := c.Middleware(func(ctx *fasthttp.RequestCtx) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
					ctx.SetBodyString("old")
					return
				}
				ctx.SetBodyString("fresh")
			})
			old := requestContext(t, "/item", map[string]string{"Host": "test"})
			go func() { h(old); close(oldDone) }()
			<-entered
			c.flights.mu.Lock()
			var flight *responseFlight
			for _, entry := range c.flights.items {
				flight = entry
			}
			c.flights.mu.Unlock()
			if flight == nil {
				t.Fatal("missing active flight")
			}
			waiter := requestContext(t, "/item", map[string]string{"Host": "test"})
			waiterDone := make(chan struct{})
			go func() { h(waiter); close(waiterDone) }()
			if closing {
				c.Close()
			} else {
				c.Clear()
			}
			select {
			case <-flight.done:
			default:
				t.Fatal("waiters were not woken")
			}
			select {
			case <-waiterDone:
			case <-time.After(time.Second):
				t.Fatal("waiter blocked after reset")
			}
			if string(waiter.Response.Body()) != "fresh" {
				t.Fatal("waiter saw stale response")
			}
			once.Do(func() { close(release) })
			<-oldDone
			check := requestContext(t, "/item", map[string]string{"Host": "test"})
			h(check)
			if string(check.Response.Body()) != "fresh" {
				t.Fatal("old worker refilled cache")
			}
			if closing {
				if calls.Load() != 3 {
					t.Fatal("closed cache did not bypass", calls.Load())
				}
				if !errors.Is(c.Clear(), ErrCacheClosed) || !errors.Is(c.Invalidate("test", "/item"), ErrCacheClosed) {
					t.Fatal("closed API state")
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				c.cache.mu.Lock()
				empty := len(c.cache.items) == 0 && len(c.cache.groups) == 0 && c.cache.bytes == 0 && c.cache.timer == nil
				c.cache.mu.Unlock()
				if !empty {
					t.Fatal("Close retained snapshots/index/timer")
				}
			}
			assertCacheIndex(t, c)
		})
	}
}

func TestCacheConcurrentResetAndInvalidation(t *testing.T) {
	c := NewCacheHandler(CacheConfig{Duration: time.Hour, MaxEntries: 32})
	h := c.Middleware(func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("public") })
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Go(func() {
			var ctx fasthttp.RequestCtx
			defer ctx.Response.Reset()
			for i := range 200 {
				uri := fmt.Sprint("/", worker, "/", i%20)
				ctx.Request.SetRequestURI("http://test" + uri)
				ctx.Response.Reset()
				h(&ctx)
				c.Invalidate("test", uri)
			}
		})
	}
	for range 20 {
		if err := c.Clear(); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	workers.Wait()
	assertCacheIndex(t, c)
	if c.cache.timer != nil || c.cache.bytes != 0 {
		t.Fatal("close did not reclaim cache")
	}
}

func BenchmarkPerformanceCacheWriteInvalidation(b *testing.B) {
	for _, count := range []int{0, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			c := NewCacheHandler(CacheConfig{Duration: time.Hour, MaxEntries: 10000})
			defer c.Close()
			expires := time.Now().Add(time.Hour)
			for i := range count {
				c.cache.put(responseCacheKey{host: "test", uri: fmt.Sprint("/read/", i)}, responseSnapshot{body: []byte("public")}, 512, expires)
			}
			h := c.Middleware(func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(204) })
			var ctx fasthttp.RequestCtx
			ctx.Request.SetRequestURI("http://test/write")
			ctx.Request.Header.SetMethod("POST")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx.Response.Reset()
				h(&ctx)
			}
		})
	}
}
