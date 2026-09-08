package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

var performanceParam string

func BenchmarkPerformanceParams(b *testing.B) {
	for _, n := range []int{0, 16, 64} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			cfg := DefaultConfig()
			cfg.RunMode = RunModeRelease
			app := NewApp(cfg)
			h := app.wrapHandler(func(c *RequestContext) { performanceParam = c.GetParam("id") })
			var raw fasthttp.RequestCtx
			for i := 0; i < n; i++ {
				raw.SetUserValue(fmt.Sprintf("metadata-%d", i), "value")
			}
			raw.SetUserValue("id", "12345")
			h(&raw)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h(&raw)
			}
		})
	}
}

func BenchmarkPerformanceJSON(b *testing.B) {
	for _, size := range []int{128, 4096, 65536} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			data := &struct {
				Text string `json:"text"`
			}{strings.Repeat("a", size)}
			c := &RequestContext{RequestCtx: &fasthttp.RequestCtx{}}
			if err := c.JSON(200, data); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Response.Reset()
				if err := c.JSON(200, data); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			c.Response.Reset()
		})
	}
}
