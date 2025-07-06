package main

import (
	"fmt"
	"log"
	"runtime"
	"strconv"

	"github.com/zjxubinbin/bingo/core"
)

func main() {
	// 创建多核优化配置
	config := &core.Config{
		Host: "0.0.0.0",
		Port: 8080,
		MultiCore: core.MultiCoreConfig{
			Enabled:           true,
			NumCPU:            0, // 使用所有核心
			WorkersPerCore:    4,
			EnableCPUAffinity: false,
			MaxConns:          10000,
			ReadBufferSize:    8192, // 增大缓冲区
			WriteBufferSize:   8192,
		},
	}

	// 创建应用实例
	app := core.NewApp(config)

	// 注册性能测试路由
	app.GET("/", func(ctx *core.RequestContext) {
		ctx.HTML(200, fmt.Sprintf(`
			<!DOCTYPE html>
			<html>
			<head>
				<title>Bingo 多核性能测试</title>
				<style>
					body { font-family: Arial, sans-serif; margin: 40px; }
					.container { max-width: 800px; margin: 0 auto; }
					.stats { background: #f8f9fa; padding: 20px; border-radius: 8px; margin: 20px 0; }
					.test-btn { display: inline-block; margin: 10px; padding: 15px 25px; background: #28a745; color: white; text-decoration: none; border-radius: 5px; }
					.test-btn:hover { background: #218838; }
				</style>
			</head>
			<body>
				<div class="container">
					<h1>🚀 Bingo 多核性能测试</h1>
					
					<div class="stats">
						<h2>系统信息</h2>
						<p><strong>CPU核心数:</strong> %d</p>
						<p><strong>GOMAXPROCS:</strong> %d</p>
						<p><strong>最大并发连接:</strong> 10,000</p>
						<p><strong>缓冲区大小:</strong> 8KB</p>
					</div>

					<h2>性能测试接口</h2>
					<a href="/ping" class="test-btn">基础性能测试</a>
					<a href="/json" class="test-btn">JSON序列化测试</a>
					<a href="/compute" class="test-btn">CPU密集计算测试</a>
					<a href="/concurrent" class="test-btn">并发处理测试</a>
					
					<h2>压力测试命令</h2>
					<pre>
# 使用wrk进行压力测试
wrk -t12 -c400 -d30s http://localhost:8080/ping

# 使用ab进行压力测试  
ab -n 10000 -c 100 http://localhost:8080/ping
					</pre>
				</div>
			</body>
			</html>
		`, runtime.NumCPU(), runtime.GOMAXPROCS(0)))
	})

	// 基础性能测试
	app.GET("/ping", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"status":    "success",
			"message":   "pong",
			"timestamp": "2024-01-01T00:00:00Z",
		})
	})

	// JSON序列化性能测试
	app.GET("/json", func(ctx *core.RequestContext) {
		data := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"items": make([]map[string]interface{}, 100),
			},
		}

		// 生成测试数据
		for i := 0; i < 100; i++ {
			data["data"].(map[string]interface{})["items"].([]map[string]interface{})[i] = map[string]interface{}{
				"id":     i,
				"name":   "Item " + strconv.Itoa(i),
				"value":  float64(i) * 1.5,
				"active": i%2 == 0,
			}
		}

		ctx.JSON(200, data)
	})

	// CPU密集计算测试
	app.GET("/compute", func(ctx *core.RequestContext) {
		// 模拟CPU密集计算
		result := 0
		for i := 0; i < 1000000; i++ {
			result += i * i
		}

		ctx.JSON(200, map[string]interface{}{
			"status":  "success",
			"result":  result,
			"message": "CPU密集计算完成",
		})
	})

	// 并发处理测试
	app.GET("/concurrent", func(ctx *core.RequestContext) {
		// 模拟并发处理
		results := make(chan int, 10)
		for i := 0; i < 10; i++ {
			go func(id int) {
				sum := 0
				for j := 0; j < 10000; j++ {
					sum += j
				}
				results <- sum
			}(i)
		}

		total := 0
		for i := 0; i < 10; i++ {
			total += <-results
		}

		ctx.JSON(200, map[string]interface{}{
			"status":  "success",
			"total":   total,
			"message": "并发处理完成",
		})
	})

	log.Printf("🚀 Bingo多核性能测试服务器启动在 %s:%d", config.Host, config.Port)
	log.Printf("🔧 多核优化已启用，使用 %d 个CPU核心", runtime.GOMAXPROCS(0))

	if err := app.Run(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
