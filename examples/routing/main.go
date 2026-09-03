package main

import (
	"fmt"
	"log"
	"strconv"

	"github.com/BinaryBinx/bingo/core"
)

// User 用户结构体
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// Product 产品结构体
type Product struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	Category string  `json:"category"`
}

func main() {
	// 创建应用实例
	app := core.NewApp(nil)

	// 1. 基础路径参数路由
	app.GET("/", func(ctx *core.RequestContext) {
		ctx.HTML(200, `
			<!DOCTYPE html>
			<html>
			<head>
				<title>Bingo 路由参数示例</title>
				<style>
					body { font-family: Arial, sans-serif; margin: 40px; }
					.container { max-width: 1000px; margin: 0 auto; }
					.section { background: #f8f9fa; padding: 20px; border-radius: 8px; margin: 20px 0; }
					.test-link { display: inline-block; margin: 10px; padding: 10px; background: #007bff; color: white; text-decoration: none; border-radius: 5px; }
					.test-link:hover { background: #0056b3; }
					.code { background: #e9ecef; padding: 10px; border-radius: 5px; font-family: monospace; }
				</style>
			</head>
			<body>
				<div class="container">
					<h1>🚀 Bingo 路由参数示例</h1>
					
					<div class="section">
						<h2>1. 路径参数路由</h2>
						<p>使用 <code>{param}</code> 语法定义路径参数</p>
						<a href="/user/123" class="test-link">用户详情: /user/123</a>
						<a href="/user/456/profile" class="test-link">用户资料: /user/456/profile</a>
						<a href="/product/789" class="test-link">产品详情: /product/789</a>
						<a href="/product/789/reviews" class="test-link">产品评论: /product/789/reviews</a>
					</div>

					<div class="section">
						<h2>2. 多级路径参数</h2>
						<p>支持多级嵌套的路径参数</p>
						<a href="/api/v1/users/123" class="test-link">API用户: /api/v1/users/123</a>
						<a href="/api/v1/products/789" class="test-link">API产品: /api/v1/products/789</a>
						<a href="/api/v1/categories/electronics" class="test-link">API分类: /api/v1/categories/electronics</a>
					</div>

					<div class="section">
						<h2>3. 查询参数测试</h2>
						<p>使用查询字符串传递参数</p>
						<a href="/search?q=golang&page=1&limit=10" class="test-link">搜索: /search?q=golang&page=1&limit=10</a>
						<a href="/filter?category=electronics&min_price=100&max_price=1000" class="test-link">筛选: /filter?category=electronics&min_price=100&max_price=1000</a>
					</div>

					<div class="section">
						<h2>4. 路由组示例</h2>
						<p>使用路由组组织相关路由</p>
						<a href="/admin/users" class="test-link">管理用户: /admin/users</a>
						<a href="/admin/products" class="test-link">管理产品: /admin/products</a>
						<a href="/admin/settings" class="test-link">管理设置: /admin/settings</a>
					</div>

					<div class="section">
						<h2>5. 测试命令</h2>
						<div class="code">
# 路径参数测试
curl http://localhost:8080/user/123
curl http://localhost:8080/product/789

# 查询参数测试
curl "http://localhost:8080/search?q=golang&page=1&limit=10"
curl "http://localhost:8080/filter?category=electronics&min_price=100&max_price=1000"

# API路由测试
curl http://localhost:8080/api/v1/users/123
curl http://localhost:8080/api/v1/products/789

# 管理路由测试
curl http://localhost:8080/admin/users
curl http://localhost:8080/admin/products
						</div>
					</div>
				</div>
			</body>
			</html>
		`)
	})

	// 2. 基础路径参数路由
	app.GET("/user/{id}", func(ctx *core.RequestContext) {
		userID := ctx.GetParam("id")
		ctx.JSON(200, map[string]interface{}{
			"message": "获取用户详情",
			"user_id": userID,
			"user": User{
				ID:   123,
				Name: "张三",
				Age:  25,
			},
		})
	})

	app.GET("/user/{id}/profile", func(ctx *core.RequestContext) {
		userID := ctx.GetParam("id")
		ctx.JSON(200, map[string]interface{}{
			"message": "获取用户资料",
			"user_id": userID,
			"profile": map[string]interface{}{
				"avatar":   "https://example.com/avatar.jpg",
				"bio":      "热爱编程的开发者",
				"location": "北京",
				"website":  "https://example.com",
			},
		})
	})

	app.GET("/product/{id}", func(ctx *core.RequestContext) {
		productID := ctx.GetParam("id")
		ctx.JSON(200, map[string]interface{}{
			"message":    "获取产品详情",
			"product_id": productID,
			"product": Product{
				ID:       789,
				Name:     "高性能Go Web框架",
				Price:    99.99,
				Category: "软件开发",
			},
		})
	})

	app.GET("/product/{id}/reviews", func(ctx *core.RequestContext) {
		productID := ctx.GetParam("id")
		ctx.JSON(200, map[string]interface{}{
			"message":    "获取产品评论",
			"product_id": productID,
			"reviews": []map[string]interface{}{
				{"user": "用户A", "rating": 5, "comment": "非常好用的框架！"},
				{"user": "用户B", "rating": 4, "comment": "性能很优秀"},
				{"user": "用户C", "rating": 5, "comment": "推荐使用"},
			},
		})
	})

	// 3. 多级路径参数路由
	apiV1 := app.Group("/api/v1")

	apiV1.GET("/users/{id}", func(ctx *core.RequestContext) {
		userID := ctx.GetParam("id")
		ctx.JSON(200, map[string]interface{}{
			"message": "API获取用户信息",
			"user_id": userID,
			"version": "v1",
			"data": User{
				ID:   123,
				Name: "API用户",
				Age:  30,
			},
		})
	})

	apiV1.GET("/products/{id}", func(ctx *core.RequestContext) {
		productID := ctx.GetParam("id")
		ctx.JSON(200, map[string]interface{}{
			"message":    "API获取产品信息",
			"product_id": productID,
			"version":    "v1",
			"data": Product{
				ID:       789,
				Name:     "API产品",
				Price:    199.99,
				Category: "API分类",
			},
		})
	})

	apiV1.GET("/categories/{name}", func(ctx *core.RequestContext) {
		categoryName := ctx.GetParam("name")
		ctx.JSON(200, map[string]interface{}{
			"message":  "API获取分类信息",
			"category": categoryName,
			"version":  "v1",
			"data": map[string]interface{}{
				"name":          categoryName,
				"description":   "这是一个产品分类",
				"product_count": 150,
			},
		})
	})

	// 4. 查询参数处理
	app.GET("/search", func(ctx *core.RequestContext) {
		query := ctx.GetQuery("q")
		pageStr := ctx.GetQuery("page")
		limitStr := ctx.GetQuery("limit")

		page := 1
		limit := 10

		if pageStr != "" {
			if p, err := strconv.Atoi(pageStr); err == nil {
				page = p
			}
		}

		if limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil {
				limit = l
			}
		}

		ctx.JSON(200, map[string]interface{}{
			"message": "搜索结果",
			"query":   query,
			"page":    page,
			"limit":   limit,
			"results": []map[string]interface{}{
				{"title": "Go语言编程", "url": "https://example.com/go"},
				{"title": "Web开发指南", "url": "https://example.com/web"},
				{"title": "高性能服务器", "url": "https://example.com/server"},
			},
		})
	})

	app.GET("/filter", func(ctx *core.RequestContext) {
		category := ctx.GetQuery("category")
		minPriceStr := ctx.GetQuery("min_price")
		maxPriceStr := ctx.GetQuery("max_price")

		minPrice := 0.0
		maxPrice := 1000.0

		if minPriceStr != "" {
			if p, err := strconv.ParseFloat(minPriceStr, 64); err == nil {
				minPrice = p
			}
		}

		if maxPriceStr != "" {
			if p, err := strconv.ParseFloat(maxPriceStr, 64); err == nil {
				maxPrice = p
			}
		}

		ctx.JSON(200, map[string]interface{}{
			"message": "筛选结果",
			"filters": map[string]interface{}{
				"category":  category,
				"min_price": minPrice,
				"max_price": maxPrice,
			},
			"products": []Product{
				{ID: 1, Name: "产品A", Price: 150.0, Category: category},
				{ID: 2, Name: "产品B", Price: 250.0, Category: category},
				{ID: 3, Name: "产品C", Price: 350.0, Category: category},
			},
		})
	})

	// 5. 管理路由组
	admin := app.Group("/admin")

	admin.GET("/users", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"message": "管理用户列表",
			"users": []User{
				{ID: 1, Name: "管理员A", Age: 30},
				{ID: 2, Name: "管理员B", Age: 28},
				{ID: 3, Name: "管理员C", Age: 35},
			},
		})
	})

	admin.GET("/products", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"message": "管理产品列表",
			"products": []Product{
				{ID: 1, Name: "管理产品A", Price: 100.0, Category: "管理分类"},
				{ID: 2, Name: "管理产品B", Price: 200.0, Category: "管理分类"},
				{ID: 3, Name: "管理产品C", Price: 300.0, Category: "管理分类"},
			},
		})
	})

	admin.GET("/settings", func(ctx *core.RequestContext) {
		ctx.JSON(200, map[string]interface{}{
			"message": "管理设置",
			"settings": map[string]interface{}{
				"site_name":   "Bingo管理后台",
				"max_users":   1000,
				"maintenance": false,
				"version":     "1.0.0",
			},
		})
	})

	// 6. 动态路由参数处理
	app.GET("/dynamic/{type}/{id}/{action}", func(ctx *core.RequestContext) {
		resourceType := ctx.GetParam("type")
		resourceID := ctx.GetParam("id")
		action := ctx.GetParam("action")

		ctx.JSON(200, map[string]interface{}{
			"message": "动态路由处理",
			"type":    resourceType,
			"id":      resourceID,
			"action":  action,
			"result":  fmt.Sprintf("对%s类型的资源%s执行%s操作", resourceType, resourceID, action),
		})
	})

	log.Printf("🚀 Bingo路由参数示例服务器启动在 :8080")
	log.Printf("📖 访问 http://localhost:8080 查看路由示例页面")

	if err := app.Run(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
