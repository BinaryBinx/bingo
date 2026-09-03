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
// 注意：路由参数语法为 {param}（fasthttp/router），旧版 :id 实际返回 404、
// handler 从未执行，不构成参数路由基准
func BenchmarkHandleRequestWithParams(b *testing.B) {
	config := DefaultConfig()
	config.RunMode = RunModeRelease
	app := NewApp(config)

	var paramValue string
	app.GET("/users/{id}", func(ctx *RequestContext) {
		paramValue = ctx.GetParam("id")
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
	if reqCtx.Response.StatusCode() != 200 {
		b.Fatalf("expected status 200, got %d", reqCtx.Response.StatusCode())
	}
	if paramValue != "12345" {
		b.Fatalf("expected handler to run with id=12345, got %q", paramValue)
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
