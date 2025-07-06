# Bingo 高性能Go Web框架

Bingo 是基于 [fasthttp](https://github.com/valyala/fasthttp)、[fasthttp/router](https://github.com/fasthttp/router)、[sonic](https://github.com/bytedance/sonic) 以及 [nhooyr/websocket](https://github.com/nhooyr/websocket) 构建的高性能Web框架，适合构建高并发、低延迟的API服务和WebSocket应用。

## 主要特性
- 超高性能 HTTP 服务器（fasthttp）
- 灵活的路由系统（fasthttp/router）
- 极速 JSON 编解码（bytedance/sonic）
- 原生 WebSocket 支持（nhooyr/websocket）
- **多核性能优化** - 自动利用所有CPU核心
- 支持中间件链、路由分组、优雅关机
- 代码结构清晰，易于扩展

## 目录结构
```
.
├── cmd/                # 可选：命令行入口
├── docs/               # 可选：文档
├── examples/           # 示例代码
│   ├── basic/          # 基础REST API示例
│   ├── websocket/      # WebSocket聊天室示例
│   ├── middleware/     # 中间件用法示例
│   ├── multicore/      # 多核性能测试示例
│   ├── routing/        # 路由参数示例
│   └── runmode/        # 运行模式示例
├── pkg/
│   ├── core/           # 框架核心（App、路由、上下文等）
│   ├── middleware/     # 常用中间件
│   └── websocket/      # WebSocket支持
├── go.mod              # Go模块定义
└── README.md           # 项目说明
```

## 依赖组件
- github.com/valyala/fasthttp
- github.com/fasthttp/router
- github.com/bytedance/sonic
- nhooyr.io/websocket

## 快速开始

### 1. 运行基础REST API示例
```bash
cd examples/basic
# 启动服务
go run main.go
# 访问接口
curl http://localhost:8080/ping
curl -X POST http://localhost:8080/user -d '{"id":1,"name":"Tom"}' -H 'Content-Type: application/json'
```

### 2. 运行WebSocket聊天室示例
```bash
cd examples/websocket
# 启动服务
go run main.go
# 使用WebSocket客户端连接 ws://localhost:8080/ws
```

### 3. 运行中间件示例
```bash
cd examples/middleware
# 启动服务
go run main.go
# 访问接口
curl http://localhost:8080/hello
```

### 4. 运行多核性能测试
```bash
cd examples/multicore
# 启动服务
go run main.go
# 访问测试页面
curl http://localhost:8080/
# 测试性能接口
curl http://localhost:8080/ping
curl http://localhost:8080/json
curl http://localhost:8080/compute
curl http://localhost:8080/concurrent
```

### 5. 运行路由参数示例
```bash
cd examples/routing
# 启动服务
go run main.go
# 访问测试页面
curl http://localhost:8080/
# 测试路径参数
curl http://localhost:8080/user/123
curl http://localhost:8080/product/789
# 测试查询参数
curl "http://localhost:8080/search?q=golang&page=1&limit=10"
# 测试API路由
curl http://localhost:8080/api/v1/users/456
```

### 6. 运行模式示例
```bash
cd examples/runmode
# 启动服务
go run main.go
# 测试不同模式
curl http://localhost:8080/  # 调试模式 (启用日志)
curl http://localhost:8081/  # 生产模式 (禁用日志)
curl http://localhost:8082/  # 测试模式 (禁用日志)
```

## 示例

### 基础示例 (examples/basic)
简单的REST API示例，包含GET、POST、PUT、DELETE操作。

```bash
cd examples/basic
go run main.go
```

访问 http://localhost:8080 查看API文档。

### WebSocket聊天室 (examples/websocket)
实时聊天室应用，支持多用户在线聊天、系统消息、特殊命令等功能。

**特性：**
- 🎨 美观的现代化UI界面
- 💬 实时消息广播
- 👥 用户在线状态显示
- 🔧 特殊命令支持 (/help, /users, /time)
- 📱 响应式设计，支持移动端
- 🔄 自动重连机制
- 📊 实时用户数量统计

**启动方式：**
```bash
cd examples/websocket
go run main.go
```

**使用方法：**
1. 打开浏览器访问 http://localhost:8080
2. 自动连接到聊天室
3. 输入消息并发送
4. 使用特殊命令：
   - `/help` - 显示帮助信息
   - `/users` - 显示在线用户列表
   - `/time` - 显示当前时间

**测试客户端：**
还提供了一个简单的测试客户端 `test_client.html`，可以用于调试WebSocket连接。

### 多核性能测试 (examples/multicore)
展示框架的多核性能优化功能。

```bash
cd examples/multicore
go run main.go
```

访问 http://localhost:8080 查看性能测试界面。

### 中间件示例 (examples/middleware)
展示各种中间件的使用方法。

```bash
cd examples/middleware
go run main.go
```

### 路由示例 (examples/routing)
展示带参数路由的使用方法。

```bash
cd examples/routing
go run main.go
```

### 运行模式示例 (examples/runmode)
展示不同运行模式下的行为差异。

```bash
cd examples/runmode
go run main.go
```

## 代码示例

### 注册路由和处理器
```go
// 基础路由
app.GET("/ping", func(ctx *core.RequestContext) {
    ctx.JSON(200, map[string]string{"message": "pong"})
})

// 路径参数路由 (使用 {param} 语法)
app.GET("/user/{id}", func(ctx *core.RequestContext) {
    userID := ctx.GetParam("id")
    ctx.JSON(200, map[string]interface{}{"user_id": userID})
})

// 查询参数
app.GET("/search", func(ctx *core.RequestContext) {
    query := ctx.GetQuery("q")
    ctx.JSON(200, map[string]interface{}{"query": query})
})
```

### 使用中间件
```go
app.Use(middleware.Logger())
app.Use(middleware.Recovery())
```

### 多核性能优化
```go
config := &core.Config{
    MultiCore: core.MultiCoreConfig{
        Enabled: true,
        NumCPU: 0,        // 使用所有核心
        WorkersPerCore: 4,
        MaxConns: 10000,
    },
}
app := core.NewApp(config)
```

### 运行模式配置
```go
// 调试模式 - 启用请求日志记录
config := &core.Config{
    RunMode: core.RunModeDebug,
}

// 生产模式 - 禁用请求日志记录
config := &core.Config{
    RunMode: core.RunModeRelease,
}

// 检查运行模式
if app.IsDebug() {
    // 调试模式逻辑
}
```

### WebSocket 聊天室
```go
wsUpgrader := websocket.NewWebSocketUpgrader(nil)
http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
    conn, _ := wsUpgrader.Upgrade(w, r)
    defer conn.Close()
    for {
        var msg map[string]interface{}
        if err := conn.Read(&msg); err != nil {
            break
        }
        wsUpgrader.GetManager().Broadcast(msg)
    }
})
```

## 运行模式

Bingo框架支持三种运行模式：

### Debug模式 (默认)
- 启用详细的请求日志记录
- 适合开发和调试环境
- 提供完整的错误信息和调试输出

### Release模式
- 禁用请求日志记录以提高性能
- 启用生产环境优化：
  - **超时优化**: 读取15s, 写入15s, 空闲30s
  - **缓冲区优化**: 读取8KB, 写入8KB
  - **并发优化**: 最大连接50,000
  - **工作协程**: 每核心8个
  - **请求体限制**: 16MB
  - **日志级别**: warn
  - **服务器名称**: Bingo-Production

### Test模式
- 禁用请求日志记录
- 适合测试环境
- 提供稳定的测试环境

```go
// 设置运行模式
config := core.DefaultConfig()
config.RunMode = core.RunModeRelease  // 生产模式
app := core.NewApp(config)
```

## 贡献与交流
欢迎提交Issue、PR或交流建议！

## License
MIT 