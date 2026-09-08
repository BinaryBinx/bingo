package main

import (
	"context"
	"log"
	"time"

	"github.com/BinaryBinx/bingo/core"
	"github.com/BinaryBinx/bingo/middleware"
)

func main() {
	// 创建应用实例
	config := core.DefaultConfig()
	// 请求日志由 Logger 中间件负责，避免同时启用 Debug 模式内置请求日志。
	config.RunMode = core.RunModeRelease
	app := core.NewApp(config)

	// 最外层统一管理业务超时和协作取消。
	app.Use(middleware.Timeout(2 * time.Second))
	// 使用日志中间件
	app.Use(middleware.Logger())
	// 使用恢复中间件
	app.Use(middleware.Recovery())
	// 使用请求ID中间件
	app.Use(middleware.RequestID())
	// 使用简单限流中间件
	app.Use(middleware.RateLimit(5)) // 每秒最多5个请求
	// 本示例只提供公开 GET 接口。先注册缓存，让命中直接复用压缩后的响应；
	// 请求 ID、限流和日志仍在缓存外层，每个请求都会执行。
	responses := middleware.NewCacheHandler(middleware.CacheConfig{
		Duration: time.Minute,
		// 公开响应不依赖额外请求头，忽略每次变化的追踪 ID 来合并回源。
		CoalesceHeaders: []string{},
	})
	if err := app.OnShutdown(func(context.Context) error { return responses.Close() }); err != nil {
		responses.Close()
		log.Fatal(err)
	}
	app.Use(responses.Middleware)
	// 只限制真正回源的业务；即使返回 408，尚未退出的业务仍占用配额。
	app.Use(middleware.ConcurrencyLimit(64))
	app.Use(middleware.CompressWithConfig(middleware.CompressConfig{
		Level:   1,
		MinSize: 512,
	}))

	// 注册GET路由
	app.GET("/hello", func(ctx *core.RequestContext) {
		ctx.String(200, "Hello, Middleware!")
	})

	// 启动服务器
	if err := app.Run(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
