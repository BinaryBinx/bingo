package core

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkHandleRequest 基准测试：完整请求处理（无路径参数，release模式）
func BenchmarkHandleRequest(b *testing.B) {
	config := DefaultConfig()
	config.RunMode = RunModeRelease
	app := NewApp(config)

	app.GET("/bench", func(ctx *RequestContext) {
		ctx.String(200, "ok")
	})

	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetMethod("GET")
	reqCtx.Request.SetRequestURI("/bench")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqCtx.Response.Reset()
		app.handleRequest(reqCtx)
	}
}

// BenchmarkHandleRequestWithParams 基准测试：带路径参数的完整请求处理
func BenchmarkHandleRequestWithParams(b *testing.B) {
	config := DefaultConfig()
	config.RunMode = RunModeRelease
	app := NewApp(config)

	app.GET("/users/:id", func(ctx *RequestContext) {
		ctx.String(200, "%s", ctx.GetParam("id"))
	})

	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetMethod("GET")
	reqCtx.Request.SetRequestURI("/users/12345")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqCtx.Response.Reset()
		app.handleRequest(reqCtx)
	}
}

// BenchmarkJSON 基准测试：JSON序列化响应
func BenchmarkJSON(b *testing.B) {
	config := DefaultConfig()
	config.RunMode = RunModeRelease
	app := NewApp(config)

	app.GET("/json", func(ctx *RequestContext) {
		_ = ctx.JSON(200, map[string]interface{}{
			"message": "hello",
			"code":    0,
		})
	})

	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetMethod("GET")
	reqCtx.Request.SetRequestURI("/json")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqCtx.Response.Reset()
		app.handleRequest(reqCtx)
	}
}
