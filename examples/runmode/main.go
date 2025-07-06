package main

import (
	"log"
	"time"

	"github.com/zjxubinbin/bingo/core"
	"github.com/zjxubinbin/bingo/middleware"
)

func main() {
	log.Println("🎯 运行模式示例 - 展示不同模式下的行为差异")

	// 创建生产模式配置
	config := core.DefaultConfig()
	config.RunMode = core.RunModeRelease
	config.Host = "0.0.0.0"
	config.Port = 8080

	// 创建应用实例
	app := core.NewApp(config)

	// 添加中间件
	app.Use(middleware.CORS([]string{"*"}, []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}, []string{"Content-Type", "Authorization"}))
	app.Use(middleware.Recovery())
	// app.Use(middleware.RequestID())

	// 注册路由
	app.GET("/", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"message":   "欢迎使用Bingo框架",
			"run_mode":  app.GetRunMode(),
			"timestamp": time.Now().Unix(),
			"server":    app.GetServerName(),
			"log_level": app.GetLogLevel(),
			"optimized": "生产环境优化已启用",
		})
	})

	app.GET("/config", func(ctx *core.RequestContext) {
		multiCore := app.GetMultiCoreConfig()
		ctx.JSON(200, map[string]interface{}{
			"run_mode":         app.GetRunMode(),
			"server_name":      app.GetServerName(),
			"log_level":        app.GetLogLevel(),
			"read_timeout":     app.GetReadTimeout().String(),
			"write_timeout":    app.GetWriteTimeout().String(),
			"idle_timeout":     app.GetIdleTimeout().String(),
			"max_request_body": app.GetMaxRequestBodySize(),
			"multi_core": map[string]interface{}{
				"enabled":          multiCore.Enabled,
				"num_cpu":          multiCore.NumCPU,
				"workers_per_core": multiCore.WorkersPerCore,
				"max_conns":        multiCore.MaxConns,
				"read_buffer":      multiCore.ReadBufferSize,
				"write_buffer":     multiCore.WriteBufferSize,
			},
		})
	})

	app.GET("/performance", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"message": "性能测试端点",
			"optimizations": []string{
				"超时优化: 读取15s, 写入15s, 空闲30s",
				"缓冲区优化: 读取8KB, 写入8KB",
				"并发优化: 最大连接50,000",
				"工作协程: 每核心8个",
				"请求体限制: 16MB",
				"日志级别: warn",
			},
		})
	})

	app.GET("/health", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"status":    "healthy",
			"mode":      app.GetRunMode(),
			"timestamp": time.Now().Unix(),
		})
	})

	// 启动服务器
	log.Printf("🏭 生产模式服务器启动中...")
	log.Printf("📊 生产环境优化包括:")
	log.Printf("   • 超时优化: 读取%s, 写入%s, 空闲%s",
		app.GetReadTimeout().String(),
		app.GetWriteTimeout().String(),
		app.GetIdleTimeout().String())
	log.Printf("   • 缓冲区优化: 读取%dKB, 写入%dKB",
		app.GetMultiCoreConfig().ReadBufferSize/1024,
		app.GetMultiCoreConfig().WriteBufferSize/1024)
	log.Printf("   • 并发优化: 最大连接%d", app.GetMultiCoreConfig().MaxConns)
	log.Printf("   • 工作协程: 每核心%d个", app.GetMultiCoreConfig().WorkersPerCore)
	log.Printf("   • 请求体限制: %dMB", app.GetMaxRequestBodySize()/(1024*1024))
	log.Printf("   • 日志级别: %s", app.GetLogLevel())
	log.Printf("   • 服务器名称: %s", app.GetServerName())

	if err := app.Run(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
