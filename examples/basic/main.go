package main

import (
	"log"

	"github.com/zjxubinbin/bingo/core"
)

// User 用户结构体
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	// 创建应用实例
	app := core.NewApp(nil)

	// 注册根路径路由
	app.GET("/", func(ctx *core.RequestContext) {
		ctx.HTML(200, `
			<!DOCTYPE html>
			<html>
			<head>
				<title>Bingo Web框架</title>
				<style>
					body { font-family: Arial, sans-serif; margin: 40px; }
					.container { max-width: 800px; margin: 0 auto; }
					.test-link { display: inline-block; margin: 10px; padding: 10px; background: #007bff; color: white; text-decoration: none; border-radius: 5px; }
					.test-link:hover { background: #0056b3; }
				</style>
			</head>
			<body>
				<div class="container">
					<h1>🚀 Bingo 高性能Web框架</h1>
					<p>欢迎使用Bingo框架！以下是可用的测试接口：</p>
					
					<h2>GET 接口测试：</h2>
					<a href="/ping" class="test-link">测试 /ping</a>
					<a href="/hello" class="test-link">测试 /hello</a>
					
					<h2>POST 接口测试：</h2>
					<p>使用以下命令测试POST接口：</p>
					<pre>curl -X POST http://localhost:8080/user -d '{"id":1,"name":"张三"}' -H 'Content-Type: application/json'</pre>
					
					<h2>JSON 响应测试：</h2>
					<a href="/json" class="test-link">测试 /json</a>
				</div>
			</body>
			</html>
		`)
	})

	// 注册GET路由
	app.GET("/ping", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]string{"message": "pong"})
	})

	// 注册hello路由
	app.GET("/hello", func(ctx *core.RequestContext) {
		ctx.String(200, "Hello, Bingo!")
	})

	// 注册JSON测试路由
	app.GET("/json", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"status":  "success",
			"message": "JSON响应测试成功",
			"data": map[string]interface{}{
				"timestamp": ctx.Time().String(),
				"version":   "1.0.0",
			},
		})
	})

	// 注册POST路由，接收JSON并返回
	app.POST("/user", func(ctx *core.RequestContext) {
		var user User
		if err := ctx.BindJSON(&user); err != nil {
			ctx.JSON(400, map[string]string{"error": err.Error()})
			return
		}
		ctx.JSON(200, user)
	})

	// 启动服务器
	if err := app.Run(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
