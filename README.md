# Bingo 高性能Go Web框架

Bingo 是基于 [fasthttp](https://github.com/valyala/fasthttp)、[fasthttp/router](https://github.com/fasthttp/router)、[sonic](https://github.com/bytedance/sonic) 以及 [coder/websocket](https://github.com/coder/websocket) 构建的高性能Web框架，适合构建高并发、低延迟的API服务和WebSocket应用。

## 主要特性
- 超高性能 HTTP 服务器（fasthttp）
- 灵活的路由系统（fasthttp/router）
- 极速 JSON 编解码（bytedance/sonic）
- 原生 WebSocket 支持（coder/websocket）
- **多核性能优化** - 保留运行时核心策略（Go 自动适配宿主与容器配额）
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
- github.com/coder/websocket
- github.com/fasthttp/websocket（示例：fasthttp 原生 WebSocket 升级）

## 快速开始

最低 Go 版本为 1.26.6；仓库推荐工具链为 Go 1.27.1（启用 `GOTOOLCHAIN=auto` 时自动选择）。

### 1. 运行基础REST API示例
```bash
cd examples/basic
# 启动服务
go run .
# 访问接口
curl http://localhost:8080/ping
curl -X POST http://localhost:8080/user -d '{"id":1,"name":"Tom"}' -H 'Content-Type: application/json'
```

### 2. 运行WebSocket聊天室示例
```bash
cd examples/websocket
# 启动服务
go run .
# 使用WebSocket客户端连接 ws://localhost:8080/ws
```

### 3. 运行中间件示例
```bash
cd examples/middleware
# 启动服务
go run .
# 访问接口
curl http://localhost:8080/hello
```

### 4. 运行多核性能测试
```bash
cd examples/multicore
# 启动服务
go run .
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
go run .
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
# 启动服务（Release 模式，监听 8080）
go run .
# 验证配置在 Release 模式下的优化生效
curl http://localhost:8080/  # 生产模式 (禁用请求日志)
curl http://localhost:8080/config
curl http://localhost:8080/health
```

## 示例

### 基础示例 (examples/basic)
简单的REST API示例，包含GET、POST、PUT、DELETE操作。

```bash
cd examples/basic
go run .
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
go run .
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
go run .
```

访问 http://localhost:8080 查看性能测试界面。

### 中间件示例 (examples/middleware)
展示各种中间件的使用方法。

```bash
cd examples/middleware
go run .
```

### 路由示例 (examples/routing)
展示带参数路由的使用方法。

```bash
cd examples/routing
go run .
```

### 运行模式示例 (examples/runmode)
展示不同运行模式下的行为差异。

```bash
cd examples/runmode
go run .
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
// 推荐：基于默认配置覆盖，避免缺少超时等默认值
config := core.DefaultConfig()
config.MultiCore.Enabled = true
config.MultiCore.NumCPU = 0 // 0 = 保留运行时现有核心策略（含容器 cgroup 自适应）
config.MultiCore.MaxConns = 10000
app := core.NewApp(config)
```
> 说明：当前 Go 运行时会考虑 Linux cgroup 配额调整 GOMAXPROCS，
> `NumCPU=0` 时不显式覆盖。`WorkersPerCore` / `EnableCPUAffinity` 为预留字段，当前版本不生效。

### 运行模式配置
```go
config := core.DefaultConfig()
config.RunMode = core.RunModeRelease // 禁用请求日志，不修改资源上限
app := core.NewApp(config)

// 检查运行模式
if app.IsDebug() {
    // 调试模式逻辑
}
```

### WebSocket (net/http 风格)
```go
// Bingo 的 websocket 包基于 net/http 风格接口（Upgrade 需要 http.Hijacker），
// 适合与标准库 http.Server 配合
wsUpgrader := websocket.NewWebSocketUpgrader(nil)
http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
    conn, err := wsUpgrader.Upgrade(w, r)
    if err != nil { return }
    defer conn.Close()
    for {
        var msg map[string]interface{}
        if err := conn.Read(&msg); err != nil {
            // 连接断开：Read 返回终止性错误时框架已自动清理连接
            break
        }
        wsUpgrader.GetManager().Broadcast(msg)
    }
})
```
### WebSocket（Bingo / fasthttp）

框架提供 `UpgradeFastHTTP`，复用相同的 Origin 校验、压缩协商和连接管理。回调在 HTTP 处理函数返回后执行，只使用传入的连接，不捕获池化的 `ctx`：

```go
app.GET("/ws", func(ctx *core.RequestContext) {
    _ = app.GetWebSocketUpgrader().UpgradeFastHTTP(ctx.RequestCtx, func(conn *websocket.Connection) {
        for {
            message, err := conn.ReadText()
            if err != nil { return }
            if err := conn.SendText(message); err != nil { return }
        }
    })
})
```

回调返回时自动关闭连接；该升级器由 App 关闭流程管理。独立创建的升级器由调用方关闭。`Upgrade` 在 HTTP 劫持后直接向套接字写握手结果，返回错误时不要再追加 HTTP 响应。

`BroadcastContext`、`BroadcastTextContext`、`BroadcastBinaryContext` 接受整个广播批次的期限并返回错误；排队也计入期限，最多 32 个发送工作协程，按需启动并在连续广播之间复用；空闲 30 秒或管理器关闭时退出，单连接广播直接执行。JSON 在批次获得发送资格后只编码一次。连接提供 `CloseWithContext`，管理器提供 `ShutdownWithContext`；到期会关闭底层连接并等待清理完成。已有 `Shutdown(ctx)` 继续保留 5 秒总上限。

WebSocket 默认读写超时仍为 30 秒，设为 0 可禁用对应操作超时。连接管理器默认空闲期限为 1 小时，收到 ping/pong 也刷新活动时间；无连接时停止清理任务。聊天示例继续使用独立的有界发送队列，以隔离慢客户端。


## 运行模式

Bingo框架支持三种运行模式：

### Debug模式 (默认)
- 启用详细的请求日志记录
- 适合开发和调试环境
- 提供完整的错误信息和调试输出

### Release模式
- 禁用请求日志记录以提高性能
- 保留显式配置的资源上限。需要下列生产预设时，显式使用 `core.ProductionConfig()`，然后覆盖自己的配置：
  - **超时优化**: 读取15s, 写入15s, 空闲30s
  - **缓冲区优化**: 读取8KB, 写入8KB
  - **并发优化**: 最大连接50,000
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

## 超时、缓存与资源配置

中间件按注册顺序从外到内执行。建议第一个注册 Timeout，让自定义中间件的响应后处理也处于同一个执行范围：

```go
app.Use(middleware.Timeout(2 * time.Second))
app.Use(middleware.Recovery())
app.Use(middleware.Logger())
// 缓存先注册，位于压缩外层：命中直接复用 gzip/identity 对应的响应。
app.Use(middleware.CacheWithConfig(middleware.CacheConfig{
    Duration: time.Minute,
    MaxEntries: 10000,
    MaxBytes: 64 << 20,
    MaxEntryBytes: 1 << 20,
}))
app.Use(middleware.Compress())
```

Timeout 超时返回 408，但不能强制终止 Go 业务函数。数据库、HTTP 等下游操作应传入 `ctx.Context()`，协作响应取消。Timeout 在内部 goroutine 恢复 panic。内置 Logger、Cache、Compress、Recovery 可安全放在 Timeout 外层；自定义外层中间件在 `next` 返回后必须先检查 `LastTimeoutErrorResponse()`，有超时响应时停止访问原请求与响应。嵌套 Timeout 由最外层统一设置期限；超时任务最终结束后会关闭遗留响应流、用户值中的 `io.Closer` 并清理 multipart 临时文件。WebSocket 升级、SSE 等长连接接口应使用独立路由/中间件配置，不套普通请求 Timeout。不要在请求返回后保留池化的 `*RequestContext`。

`Cache` 默认最多保存 10000 项、64 MiB 快照数据，每项最多 1 MiB（包括正文、键和头部估算开销）。命中查询使用 32 个分片；淘汰采用二次机会算法（近似 LRU），热点读取无需逐次修改淘汰链。条目与字节预算仍为全局上限，不按分片切割。到期自动回收，无常驻清理 goroutine。自动按 `Vary` 区分响应；额外的请求头维度使用与正文共用预算的索引，索引也计入条目上限。带 Cookie、Authorization、条件头或缓存控制头的请求跳过共享缓存；共享缓存只应安装在公开路由。`StaticWithConfig` 提供对应的静态文件预算；默认 1000 项、64 MiB 总预算、1 MiB 单文件、1 分钟保留期，命中时仍检查文件元数据。静态文件支持 `If-Modified-Since` / 304，HEAD 只返回元数据；设置 `TTL < 0` 可禁用静态文件快照缓存。缓存容量和 TTL 应按业务调整。

同一个未命中请求的并发回源会自动合并。首次还不知道 `Vary` 时，只有完整请求头相同的请求才共同等待；唤醒后每个请求重新按 `Vary` 查缓存，不直接共享另一个请求的上下文或响应。私有响应、错误、超时和已过期结果不会被转发给等待者；等待者的业务取消不会取消正在回源的请求。每条缓存处理链额外最多保留 256 个回源协调项、1 MiB 的键与元数据估算量（同时不超过配置的条目数、字节数）；达到上限直接独立执行，避免等待表无界增长。

缓存与压缩同时使用时，先注册 `Cache` / `CacheWithConfig`，再注册 `Compress`，即 `Cache(Compress(handler))`。反向顺序也保持内容正确，但缓存命中后仍需重新压缩。公开路由示例见 `examples/middleware/main.go`；日志、请求 ID、鉴权和限流等需要逐请求执行的中间件应放在缓存外层。

日志采样可减少日志 I/O：

```go
app.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
    SampleEvery: 100, // 每 100 个请求记录一次，0/1 表示全量
    Printf: log.Printf,
}))
```

自定义 `Printf` 同步执行且可能被多个请求并发调用；异步日志输出由调用方管理队列与关闭流程。`Auth` 支持大小写不敏感的 Bearer 前缀，空 token 或空校验函数返回 401，附带 `WWW-Authenticate: Bearer` 与 `Cache-Control: no-store`。

`JSON` 将独立的序列化缓冲移交给响应，避免二次复制，后续仍可追加正文或替换响应流。`GetParam` 按需读取路由用户值，`SetParam` 的本地覆盖优先；参数值仅应在本次请求中使用。

`BindJSON` 默认复制字符串，避免长期保存一个字段却留住整个 JSON 缓冲。确定所有解码字段仅在本次请求中使用时，可以在创建 App 前设置 `config.JSONCopyStrings = false`。`ReduceMemoryUsage` 也可在配置中按吞吐/内存目标选择。

`NewApp` 保存独立的配置副本，`GetConfig` 返回生效配置快照；修改原配置或返回值不会改变运行中的 Server。需要严格拒绝错误配置时使用 `NewAppChecked`；文件与环境变量加载会统一校验，失败时不提交部分环境变量。环境超时单位仍为秒，JSON 配置里的 `time.Duration` 数值单位仍为纳秒。

`SaveConfig` 使用同目录临时文件、同步写入和替换，保留已有文件权限；新文件使用 0600。Windows 使用专用替换 API，并处理长路径与替换失败后的只读临时文件清理。

`Run` 等待关闭流程完成后返回；直接使用底层 `Serve` 时，调用方需自行等待 `Shutdown`。自有的 hijack 连接或其他资源可通过 `OnShutdown(func(context.Context) error)` 注册清理，回调应遵守期限且不能递归调用 Shutdown。`ShutdownWithContext` 支持自定义总期限。

## 性能回归验证

`performance_bench_test.go` 覆盖参数按需读取、不同大小的 JSON、缓存并发命中、短响应压缩、中间件顺序，以及 WebSocket 上下文和广播调度。可使用同一台机器、相同工具链对比修改前后：

```sh
go test ./core ./middleware ./websocket -run '^$' -bench '^BenchmarkPerformance' -benchmem -count=3
go test ./middleware -run '^$' -bench '^BenchmarkPerformanceCacheHitParallel' -benchmem '-cpu=1,8,16'
go test -race ./...
```

这些基准测量对应处理路径的 CPU 与分配开销，广播调度基准不包含网络 I/O；线上吞吐量和延迟还需要使用实际响应大小、缓存命中率、连接数及慢客户端比例做端到端压测。

## 贡献与交流
欢迎提交Issue、PR或交流建议！

## License
MIT
