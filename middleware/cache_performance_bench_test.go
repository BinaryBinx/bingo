package middleware

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func BenchmarkPerformanceCacheReplace(b *testing.B) {
	cache := newBoundedCache[int, int](1000, 1000)
	cache.groupOf = func(int) cacheGroupKey { return cacheGroupKey{"example.test", "/variants"} }
	defer cache.clear()
	expires := time.Now().Add(time.Hour)
	for key := range 1000 {
		cache.put(key, key, 1, expires)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.put(0, i, 1, expires)
	}
}

// Use a fixed count, e.g. -benchtime=200x: marking the existing entries hot is
// setup work outside the timer, so this measures the insertion/eviction itself.
func BenchmarkCacheEvictionAllHot(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			cache := newBoundedCache[int, int](count, 1<<30)
			defer cache.clear()
			expires := time.Now().Add(time.Hour)
			for i := 0; i < count; i++ {
				cache.put(i, i, 1, expires)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				for _, entry := range cache.items {
					entry.referenced.Store(true)
				}
				b.StartTimer()
				cache.put(count+i, count+i, 1, expires)
			}
		})
	}
}

func BenchmarkPerformanceCacheMissHeaders(b *testing.B) {
	for _, count := range []int{0, 32, 128} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				c.Response.Header.Set("Cache-Control", "no-store")
				c.SetBodyString("ok")
			})
			var c fasthttp.RequestCtx
			defer c.Response.Reset()
			c.Request.SetRequestURI("http://example.test/uncacheable")
			for i := 0; i < count; i++ {
				c.Request.Header.Set(fmt.Sprintf("X-Header-%d", i), "some-long-request-header-value-for-this-example")
			}
			h(&c)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Response.Reset()
				h(&c)
			}
		})
	}
}

func BenchmarkPerformanceCacheMissHeadersParallel(b *testing.B) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	defer cache.Close()
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		c.Response.Header.Set("Cache-Control", "no-store")
		c.SetBodyString("ok")
	})
	var worker atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var c fasthttp.RequestCtx
		defer c.Response.Reset()
		c.Request.SetRequestURI(fmt.Sprintf("http://example.test/worker/%d", worker.Add(1)))
		for i := 0; i < 32; i++ {
			c.Request.Header.Set(fmt.Sprintf("X-Header-%d", i), strings.Repeat("v", 48))
		}
		for pb.Next() {
			c.Response.Reset()
			h(&c)
		}
	})
}

func BenchmarkPerformanceCacheVaryHit(b *testing.B) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	defer cache.Close()
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		c.Response.Header.Set("Vary", "Accept-Language")
		c.SetBodyString("public")
	})
	var c fasthttp.RequestCtx
	defer c.Response.Reset()
	c.Request.SetRequestURI("/vary")
	c.Request.Header.Set("Accept-Language", "en")
	h(&c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Response.Reset()
		h(&c)
	}
}
