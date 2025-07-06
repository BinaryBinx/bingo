package main

import (
	"log"

	"github.com/zjxubinbin/bingo/core"
	"github.com/zjxubinbin/bingo/middleware"
)

func main() {
	// 创建应用实例
	app := core.NewApp(nil)

	// 使用日志中间件
	app.Use(middleware.Logger())
	// 使用恢复中间件
	app.Use(middleware.Recovery())
	// 使用请求ID中间件
	app.Use(middleware.RequestID())
	// 使用简单限流中间件
	app.Use(middleware.RateLimit(5)) // 每秒最多5个请求

	// 注册GET路由
	app.GET("/hello", func(ctx *core.RequestContext) {
		ctx.String(200, "Hello, Middleware!")
	})

	// 启动服务器
	if err := app.Run(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
