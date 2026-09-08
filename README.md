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

路由须在 `Run` / `Serve` 启动前注册。注册与启动使用同一把锁，启动或关闭开始后路由保持不可变，请求匹配不增加锁。`app.Handle(method, path, handler)` 和 `group.Handle(...)` 在此后返回 `core.ErrRoutesFrozen`；原有 `GET` / `POST` 等快捷方法保持原签名，并以该错误 panic，便于立即发现错误的注册时机。重复路由和非法模式仍沿用底层路由器的 panic 行为。

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
    CoalesceHeaders: []string{"Accept-Language"},
}))
// 缓存命中不消耗回源配额；需要限制所有请求时放在 Cache 前。
app.Use(middleware.ConcurrencyLimit(256))
app.Use(middleware.CompressWithConfig(middleware.CompressConfig{
    Level: 1,     // 1 优先速度；0 使用库的默认压缩级别
    MinSize: 512, // 小于 512 字节不压缩
}))
```

Timeout 超时返回 408，但不能强制终止 Go 业务函数。数据库、HTTP 等下游操作应传入 `ctx.Context()`，协作响应取消。App 在中间件之前挂载共享的标准取消上下文，避免每个 deadline 额外产生一个取消监听协程；独立使用 fasthttp 中间件时仍保留服务器关闭通道的兼容回退。Timeout 在内部 goroutine 恢复 panic。内置 Logger、Cache、Compress、Recovery 可安全放在 Timeout 外层；自定义外层中间件在 `next` 返回后必须先检查 `LastTimeoutErrorResponse()`，有超时响应时停止访问原请求与响应。嵌套 Timeout 由最外层统一设置期限；超时任务最终结束后会关闭遗留响应流、用户值中的 `io.Closer` 并清理 multipart 临时文件。后台业务和这些延迟清理均纳入 App 的停机排空。WebSocket 升级、SSE 等长连接接口应使用独立路由/中间件配置，不套普通请求 Timeout。不要在请求返回后保留池化的 `*RequestContext`。

`Cache` 默认最多保存 10000 项、64 MiB 快照数据，每项最多 1 MiB（包括正文、键和头部估算开销）。命中查询使用 32 个分片；淘汰采用二次机会算法（近似 LRU），热点读取无需逐次修改淘汰链。条目与字节预算仍为全局上限，不按分片切割。到期自动回收，无常驻清理 goroutine。自动按 `Vary` 区分响应；额外的请求头维度使用与正文共用预算的索引，索引也计入条目上限。带 Cookie、Authorization、条件头或缓存控制头的请求跳过共享缓存；共享缓存只应安装在公开路由。`StaticWithConfig` 提供对应的静态文件预算；默认 1000 项、64 MiB 总预算、1 MiB 单文件、1 分钟保留期，命中时仍检查文件元数据。静态文件支持 `If-Modified-Since` / 304，HEAD 只返回元数据；设置 `TTL < 0` 可禁用静态文件快照缓存。缓存容量和 TTL 应按业务调整。

单次写入最多检查 64 个淘汰候选，给近期命中的条目第二次机会；达到上限后直接淘汰最旧候选，避免全热缓存触发整库轮转。这个扫描预算由同一次写入的所有淘汰共用；较大的新条目仍可能需要实际移除多个旧条目来满足容量上限。响应缓存与静态快照缓存共用此策略。

缓存未命中时，用独立的连续字节缓冲保存原始请求头，确保下游改写请求后仍按原来的 `Vary` 维度入库。临时快照池在所有缓存实例之间共享，最多保留 64 个空闲对象，每个对象的字节缓冲容量不超过 16 KiB、字段索引容量不超过 256；大请求正常处理，超出上限的缓冲用完丢弃。原请求、正文及 fasthttp 内部缓冲不会被池引用；缓存命中不使用该池。

`ConcurrencyLimit(n)` 独立限制正在执行的业务数量，满额立即返回 503 和 `Retry-After: 1`，不建立等待队列；`n <= 0` 禁用。与 Bingo `Timeout` 的两种嵌套顺序均支持：已经返回 408、但还没退出的业务继续占用配额，直到业务返回或 panic 展开完成。它无法强制结束阻塞代码；流式响应和 hijack 连接在业务函数返回后的生命周期不计入该上限。同一个 `limiter := middleware.ConcurrencyLimit(n)` 包装多个处理器时共享预算；分别调用工厂则各自独立。

408 使用独立响应快照，并设置 `Cache-Control: no-store`。Timeout 进入前设置的 CORS、请求 ID 和安全头会保留；内置 CORS、RequestID、Security 及 `RequestContext.SetHeader` 在 Timeout 内层时，也会把这些允许透传的头同步到独立元数据快照。只保留截止时已经发布的值；后台业务继续修改原 Response 不会影响已发出的快照。自定义 fasthttp 中间件可在 Timeout 前设置这些公共头；Cookie、正文摘要和签名不会被复制到超时错误中。

Timeout 被 fasthttp 原生并发上限拒绝时返回 429，恢复已发布的公共响应头，并设置 `Cache-Control: no-store` 和 `Retry-After: 1`；独立使用 Timeout、没有 App 工作跟踪或 ConcurrencyLimit 时也适用。

核心 handler 的 panic 恢复与中间件拒绝/恢复路径共用错误响应重建逻辑：替换正文时清理旧实体头、摘要、签名以及原 Trailer 声明和值，保留公共安全、跨域和追踪头。需要对错误响应签名时，在错误处理外层对最终响应重新计算。

同一个未命中请求的并发回源会自动合并。`CoalesceHeaders: nil` 保留完整请求头分组，适合未知响应维度；每次变化的 `Traceparent`、请求 ID 也会区分回源。公开路由可显式配置稳定维度，例如 `[]string{"Accept-Language"}`；非 nil 空切片 `[]string{}` 只使用 Host、URI、Accept-Encoding、Origin。配置切片在创建时复制，非法字段名退回完整请求头分组。

`CoalesceHeaders` 只控制哪些请求共同等待，不替代响应的 `Vary`。唤醒后每个请求重新按实际 `Vary` 查缓存；不匹配时独立回源，不直接共享另一个请求的上下文或响应。依赖请求头的公开响应仍须正确声明 `Vary`。私有请求绕过合并；私有响应、错误、超时和已过期结果不会被转发给等待者；等待者的业务取消不会取消正在回源的请求。每条缓存处理链额外最多保留 256 个回源协调项、1 MiB 的键与元数据估算量（同时不超过配置的条目数、字节数）；达到上限直接独立执行，避免等待表无界增长。

缓存等待者因取消或期限到达返回 408 时，同样保留 CORS、请求 ID 与安全头，清理旧正文的实体元数据，并设置 `Cache-Control: no-store`。

缓存与压缩同时使用时，先注册 `Cache` / `CacheWithConfig`，再注册 `Compress`，即 `Cache(Compress(handler))`。反向顺序也保持内容正确，但缓存命中后仍需重新压缩。公开路由示例见 `examples/middleware/main.go`；日志、请求 ID、鉴权和限流等需要逐请求执行的中间件应放在缓存外层。

响应已有 `Signature` / `Signature-Input`，或声明对应 Trailer 时，缓存会跳过存储；查缓存前已经设置签名的响应也直接执行下游。字段为空或大小写不规范时仍按签名处理，防止命中时更新 `Age`、请求 ID 等使签名失效。只有正文摘要（如 `Content-Digest`）的响应仍可缓存。需要对缓存命中结果签名时，将签名中间件放在 Cache 外层，并在 `next` 返回后为最终响应重新生成签名。

成功的 PUT/POST/PATCH/DELETE 等不安全请求（包括未知方法，响应为 2xx/3xx）会自动清除目标 URI 的所有 Vary、压缩和 Origin 变体，认证写请求也会生效；安全方法和 4xx/5xx 保留缓存。同源 `Location` / `Content-Location` 目标也会失效。需要在后台任务或其他路由提交数据后主动失效时，保存缓存实例：

```go
responses := middleware.NewCacheHandler(middleware.CacheConfig{Duration: time.Minute})
if err := app.OnShutdown(func(context.Context) error { return responses.Close() }); err != nil {
    responses.Close()
    log.Fatal(err)
}
app.Use(responses.Middleware) // 在 Timeout 内层、Compress 外层
// 在数据提交成功后调用；Host 包括端口，路径包括完整查询参数。
if err := responses.Invalidate("example.test:8080", "/items?id=1"); err != nil {
    log.Printf("缓存失效失败: %v", err)
}
```

`Invalidate` 接受 origin-form 路径和查询，不接受绝对 URL 或 fragment；不同查询参数分别失效，Host 忽略大小写但端口精确匹配。失效会唤醒回源等待者，并禁止失效前的旧请求重新写回缓存；这些旧请求仍可完成自己的客户端响应。失效版本使用固定大小的槽；Host/URI 索引只覆盖实际保留的条目，写后失效只遍历目标 URL 的变体。索引开销计入字节预算，随条目淘汰、替换和到期一起回收。应把缓存放在 Timeout 内层，让超时后成功完成的写操作也能失效；如果缓存包装在 Timeout 外层，业务在最终提交后需要主动调用 `Invalidate`，外层不能安全读取仍运行的 worker 的最终状态。

`responses.Clear()` 清空快照和索引、停止到期定时器，并唤醒等待者；后续请求可以重新填充，清空前的旧回源不能写回。`responses.Close()` 永久关闭缓存并释放这些资源，关闭后的中间件直接执行下游，不再缓存或合并回源。Close 可重复调用；关闭后的 Clear / Invalidate 返回 `middleware.ErrCacheClosed`。这些操作均支持并发调用。显式管理生命周期时使用 `NewCacheHandler` 并注册 OnShutdown；同一个实例跨多个 App 共享时，由共同拥有者负责最终关闭。

替换缓存条目或 Vary 索引时，旧的未过期快照保持可读，直到新快照一次发布完成，避免先删除再插入造成额外回源。新快照在取得写锁后检查有效期，并按容量、到期和 URL 索引规则完成替换。

`Compress()` 等价于 `CompressWithConfig(CompressConfig{})`，默认最小 256 字节，只压缩文本、JSON/XML（含 `+json`/`+xml`）、JavaScript、Wasm、表单和 SVG。PNG、ZIP、音视频与 `application/octet-stream` 默认跳过，减少无收益的 gzip 运算。`ContentTypes` 支持忽略大小写和参数的媒体类型匹配，以及单个通配符；nil 使用默认列表，空切片不压缩任何类型，`[]string{"*/*"}` 显式允许所有类型。`Level` 支持 -2、-1、1～9，0 或非法值使用默认级别；`MinSize <= 0` 使用 256。`Disabled: true` 完全旁路；`Skip: func(c *fasthttp.RequestCtx) bool { return string(c.Path()) == "/download" }` 在业务执行前按路由跳过，该回调须支持并发调用。若 Skip 依据其他请求头决定，响应应声明相应 `Vary`。已有编码、流、HEAD、206/204/304 和 `no-transform` 继续受到保护。

响应含 `Content-Digest`、`Repr-Digest`、`Digest`、`Content-MD5`、`Signature`、`Signature-Input` 或声明这些字段的 Trailer 时，压缩会在改写头部之前完全跳过，保留正文及完整性元数据。需要对压缩结果生成摘要/签名时，将生成它们的中间件放在 Compress 外层，对最终表示计算。

高频静态文件服务可使用托管组件，复用目录句柄并注册关闭：

```go
files, err := middleware.NewStaticHandler("./public", middleware.StaticConfig{
    TTL: time.Minute,
    Immutable: false, // 默认检查元数据；内容带版本的 URL 可显式开启
    ETag: middleware.WeakStaticETag, // 可选：根据大小和修改时间生成弱校验器
})
if err != nil { log.Fatal(err) }
// 注册成功后由 OnShutdown 在业务排空后关闭，避免停机超时返回时提前释放。
if err := app.OnShutdown(func(context.Context) error { return files.Close() }); err != nil {
    files.Close()
    log.Fatal(err)
}
app.Use(files.Middleware)
// 发布完成后可调用 files.Reload("") 重开原目录并清空快照，
// 或 files.Reload("./releases/v2/public") 切换目录；须处理返回错误。
```

`NewStaticHandler` 在启动时校验根目录，`Reload` 打开失败保留当前目录和缓存。Reload/Close 与请求并发安全，会释放旧快照、到期定时器和根目录句柄，已打开的响应流仍可继续读取；Close 可重复调用，关闭后的静态 GET/HEAD 返回 503。`Immutable: true` 的有效快照命中不访问文件系统，文件变更直到 TTL 到期或 Reload 后可见，仅用于内容不可变的版本化 URL；浏览器缓存策略仍由应用设置。旧 `Static` / `StaticWithConfig` API 保留每请求打开根目录的行为，无需新增关闭流程。完整生命周期示例见 `examples/static/main.go`。

Immutable 大文件使用准确 Content-Length 的文件流，在支持的明文 TCP 连接上可利用 sendfile；开启后必须保证文件在发送期间不被原地修改。普通可变文件及读取中增长的文件仍使用完整流式发送。静态 GET/HEAD 按 `If-Match`、`If-Unmodified-Since`、`If-None-Match`、`If-Modified-Since` 的优先级处理条件，返回对应的 304/412。304/412 及 HEAD 响应不会读取正文。

默认支持单段 `Range: bytes=起点-终点`、`bytes=起点-` 和 `bytes=-末尾长度`，返回 206、准确的 Content-Range，并只读取选中的字节；合法但越界的请求返回 416 和 `bytes */文件大小`。HEAD 忽略 Range；多段、重复、格式错误或未知单位回退完整响应。`DisableRange: true` 可关闭此能力。If-Range 在前述条件通过后执行，强 ETag 必须精确匹配，日期也必须与 Last-Modified 精确匹配，否则返回完整表示；206 不再进行 gzip 压缩。行为顺序依据 [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html#section-13.2.2)。

`ETag: nil` 默认不生成实体标签。可设置 `ETag: middleware.WeakStaticETag`，无需扫描正文；文件变化必须更新大小或修改时间。弱标签可用于 If-None-Match/304，不能通过 If-Match 或 If-Range 的强比较。需要强标签时，提供 `func(os.FileInfo) string`，返回能标识该文件精确字节版本的引号标签；回调须并发安全，不能调用同一组件的 Reload/Close。非法标签会被忽略。实体标签列表支持重复字段、弱比较和包含逗号的合法引号标签。

WebSocket 广播继续复用最多 32 个工作协程，改为逐个领取连接，避免慢连接预占一组健康连接。单个慢连接只占用一个工作协程；达到工作协程数量的慢连接仍会受广播截止时间和背压约束。

空连接广播会跳过 JSON 编码。广播编码和传输继续串行保持顺序，但停机只等待传输任务，阻塞的自定义 `MarshalJSON` 不再拖住连接关闭。编码仍在调用者协程内同步执行，应用须自行保证它终止；框架不会创建无界编码 goroutine，也不会允许编码结束后向已关闭的管理器提交发送。聊天室示例的 `/users` 在读锁内取得用户名快照，锁外一次拼接列表。

日志采样可减少日志 I/O：

```go
app.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
    SampleEvery: 100, // 每 100 个请求记录一次，0/1 表示全量
    Printf: log.Printf,
}))
```

自定义 `Printf` 同步执行且可能被多个请求并发调用；异步日志输出由调用方管理队列与关闭流程。`Auth` 支持大小写不敏感的 Bearer 前缀，空 token 或空校验函数返回 401，附带 `WWW-Authenticate: Bearer` 与 `Cache-Control: no-store`。

`SendError` 生成 JSON 错误前会关闭旧响应流，清理旧正文的编码、摘要、签名、校验器、Trailer 及缓存策略，保留 CORS、安全、请求 ID 和认证/重试头，并设置 `Cache-Control: no-store`。静态文件读取失败，以及 WebSocket 在升级前或握手阶段拒绝请求时，也采用这套错误响应规则；握手拒绝正文为纯文本。

`JSON` 先完成序列化，再更新状态码、类型和正文。序列化失败时返回错误，保留原响应及响应流，不读取或关闭旧流；调用方应处理返回的错误。成功后将独立的序列化缓冲移交给响应，避免二次复制，后续仍可追加正文或替换响应流。`GetParam` 按需读取路由用户值，`SetParam` 的本地覆盖优先；参数值仅应在本次请求中使用。

`BindJSON` 默认复制字符串，避免长期保存一个字段却留住整个 JSON 缓冲。确定所有解码字段仅在本次请求中使用时，可以在创建 App 前设置 `config.JSONCopyStrings = false`。`ReduceMemoryUsage` 也可在配置中按吞吐/内存目标选择。

`NewApp` 保存独立的配置副本，`GetConfig` 返回生效配置快照；修改原配置或返回值不会改变运行中的 Server。需要严格拒绝错误配置时使用 `NewAppChecked`；文件与环境变量加载会统一校验，失败时不提交部分环境变量。环境超时单位仍为秒，JSON 配置里的 `time.Duration` 数值单位仍为纳秒。

`SaveConfig` 使用同目录临时文件、同步写入和替换，保留已有文件权限；新文件使用 0600。Windows 使用专用替换 API，并处理长路径与替换失败后的只读临时文件清理。

`ShutdownWithContext` 首先停止业务准入并取消应用上下文，HTTP 和 WebSocket 同时停止接入并排空；通过 `OnStopping(func(context.Context) error)` 通知生产者停止、关闭自有 hijack 连接。HTTP 响应流、WebSocket 传输和超时后台业务（包括延迟资源清理）全部结束后，才执行 `OnShutdown(func(context.Context) error)` 释放数据库池、静态目录等依赖。已有负责关闭自有 hijack 的 OnShutdown 回调应迁移到 OnStopping；聊天室示例已迁移。两阶段回调分别按注册顺序执行，须遵守 context，不能递归调用 Shutdown。

第一个关闭调用设置共同预算，其余调用可使用更短的等待期限。达到期限会返回 context 错误，`Run` 也会返回错误，不把仍运行的业务当作关闭成功。若业务忽略取消，协调器在后台等它实际退出后才执行资源清理，传入的是原始（可能已过期的）关闭 context；这需要调用方在超时返回后继续保留进程。正常情况下 `Run` 等待完整清理后返回；直接使用 `Serve` 时，它只等待监听停止，调用方仍须等待 Shutdown。

## 性能回归验证

基准覆盖参数按需读取、不同大小的 JSON、缓存并发命中与未命中、条目替换、全热缓存淘汰、短响应/二进制响应压缩、静态文件普通/托管/不可变快照、大文件 TCP 发送、deadline 父上下文、聊天室用户列表，以及 WebSocket 上下文和广播调度。全热淘汰基准需要在计时外准备条目状态，使用固定的 200 次测量。可使用同一台机器、相同工具链对比修改前后：

```sh
go test ./core ./middleware ./websocket -run '^$' -bench '^BenchmarkPerformance' -benchmem -count=3
go test ./internal/requestcontext -run '^$' -bench '^BenchmarkPerformance' -benchmem -count=3
go test ./examples/websocket -run '^$' -bench '^BenchmarkUserList$' -benchmem -count=3
go test ./middleware -run '^$' -bench '^BenchmarkStaticFileTCP$' -benchmem -count=3
go test ./middleware -run '^$' -bench '^BenchmarkPerformanceCacheHitParallel' -benchmem '-cpu=1,8,16'
go test ./middleware -run '^$' -bench '^BenchmarkCacheEvictionAllHot$' -benchmem -benchtime=200x -count=3
go test ./middleware -run '^$' -fuzz '^FuzzStaticRangeAndETag$' -fuzztime=30s
go test -race ./...
```

这些基准测量对应处理路径的 CPU 与分配开销，广播调度基准不包含网络 I/O；线上吞吐量和延迟还需要使用实际响应大小、缓存命中率、连接数及慢客户端比例做端到端压测。

## 贡献与交流
欢迎提交Issue、PR或交流建议！

## License
MIT
