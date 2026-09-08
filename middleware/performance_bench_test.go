package middleware

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
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

func BenchmarkPerformanceStaticSnapshot(b *testing.B) {
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "asset.txt"), []byte(strings.Repeat("x", 16<<10)), 0600); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"legacy", "managed", "immutable"} {
		b.Run(mode, func(b *testing.B) {
			next := func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) }
			var h fasthttp.RequestHandler
			if mode == "legacy" {
				h = Static(root)(next)
			} else {
				s, err := NewStaticHandler(root, StaticConfig{Immutable: mode == "immutable"})
				if err != nil {
					b.Fatal(err)
				}
				defer s.Close()
				h = s.Middleware(next)
			}
			var c fasthttp.RequestCtx
			defer c.Response.Reset()
			c.Request.SetRequestURI("/asset.txt")
			h(&c)
			if c.Response.StatusCode() != 200 || len(c.Response.Body()) != 16<<10 {
				b.Fatal("snapshot setup failed")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Response.Reset()
				h(&c)
			}
		})
	}
}

func BenchmarkPerformanceCompressionBinary(b *testing.B) {
	body := make([]byte, 64<<10)
	if _, err := rand.Read(body); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"baseline", "default", "all-types"} {
		b.Run(mode, func(b *testing.B) {
			leaf := func(c *fasthttp.RequestCtx) { c.SetContentType("application/octet-stream"); c.SetBody(body) }
			h := leaf
			if mode == "default" {
				h = Compress()(leaf)
			}
			if mode == "all-types" {
				h = CompressWithConfig(CompressConfig{ContentTypes: []string{"*/*"}})(leaf)
			}
			var c fasthttp.RequestCtx
			defer c.Response.Reset()
			c.Request.Header.Set("Accept-Encoding", "gzip")
			// Warm adaptive gzip state as well as pool storage before timing.
			for i := 0; i < 10000; i++ {
				c.Response.Reset()
				h(&c)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Response.Reset()
				h(&c)
			}
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
