package middleware

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func BenchmarkPerformanceCacheHitParallel(b *testing.B) {
	for _, keys := range []int{1, 64} {
		b.Run(fmt.Sprint(keys), func(b *testing.B) {
			h := Cache(time.Hour)(func(c *fasthttp.RequestCtx) { c.SetBodyString("public") })
			uris := make([]string, keys)
			for i := range uris {
				uris[i] = fmt.Sprintf("/%d", i)
				var c fasthttp.RequestCtx
				c.Request.SetRequestURI(uris[i])
				h(&c)
				c.Response.Reset()
			}
			var worker atomic.Uint64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				var c fasthttp.RequestCtx
				c.Request.SetRequestURI(uris[int(worker.Add(1)-1)%keys])
				for pb.Next() {
					c.Response.Reset()
					h(&c)
				}
			})
		})
	}
}

func BenchmarkPerformanceCompressionSmall(b *testing.B) {
	h := Compress()(func(c *fasthttp.RequestCtx) { c.SetBodyString("ok") })
	var c fasthttp.RequestCtx
	c.Request.Header.Set("Accept-Encoding", "gzip, deflate, br")
	h(&c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Response.Reset()
		h(&c)
	}
}

func BenchmarkPerformanceCacheCompressionOrder(b *testing.B) {
	for _, outerCache := range []bool{true, false} {
		b.Run(fmt.Sprint(outerCache), func(b *testing.B) {
			body := strings.Repeat("example payload ", 1024)
			leaf := func(c *fasthttp.RequestCtx) { c.SetBodyString(body) }
			h := Cache(time.Hour)(Compress()(leaf))
			if !outerCache {
				h = Compress()(Cache(time.Hour)(leaf))
			}
			var c fasthttp.RequestCtx
			c.Request.SetRequestURI("/")
			c.Request.Header.Set("Accept-Encoding", "gzip")
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
