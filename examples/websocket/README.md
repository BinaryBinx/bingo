# Bingo WebSocket 聊天室示例

## 概述

这个示例展示了如何使用Bingo框架实现一个功能完整的实时聊天室应用。相比之前的实现，现在完全基于Bingo框架，充分利用了框架的高性能特性。

## 主要改进

### 1. 使用Bingo框架
- ✅ 使用 `core.App` 作为应用入口
- ✅ 使用 `core.RequestContext` 处理请求
- ✅ 集成Bingo框架的中间件系统
- ✅ 使用框架的路由系统

### 2. 架构优化
- ✅ 模块化设计，代码结构清晰
- ✅ 使用 fasthttp 原生 WebSocket 升级（FastHTTPUpgrader）
- ✅ 统一的错误处理和日志记录
- ✅ 支持框架的配置系统

### 3. 功能增强
- ✅ 用户颜色系统（13种预定义颜色）
- ✅ 特殊命令支持（/help, /users, /time）
- ✅ 实时用户统计
- ✅ 自动重连机制
- ✅ 现代化UI界面

## 快速开始

### 运行示例

```bash
# 进入示例目录
cd examples/websocket

# 编译并运行
go run .
```

### 访问应用

打开浏览器访问: http://localhost:8080

## 核心代码结构

### 1. 应用初始化

```go
// 创建Bingo应用
app := core.NewApp(nil)

// 创建聊天室
chatRoom := NewChatRoom(app)

// 添加中间件
app.Use(middleware.CORS([]string{"*"}, []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}, []string{"Content-Type", "Authorization"}))
app.Use(middleware.Recovery())
app.Use(middleware.RequestID())
```

### 2. 路由注册

```go
// 静态页面
app.GET("/", func(ctx *core.RequestContext) {
    ctx.HTML(200, chatRoomHTML)
})

// WebSocket端点
app.GET("/ws", func(ctx *core.RequestContext) {
    chatRoom.HandleWebSocket(ctx)
})
```

### 3. WebSocket处理

`HandleWebSocket` 在升级前保存实际 TCP 连接，并在 fasthttp 原生升级回调中创建 `chatClient`。fasthttp 的 hijack 包装连接在回调退出前可能忽略 `Close`，因此主动关闭通过保存的底层连接执行，以解除阻塞的读取。

每个客户端有一个写入协程。欢迎消息、命令回复和广播全部经过同一发送队列，最多 64 条、256 KiB 待发送正文；队列满或写入超过 5 秒会断开慢客户端。单条入站消息最多 64 KiB，聊天正文最多 500 个字符；仅接收 message/ping 类型。聊天室最多 1000 个连接，默认只允许同源握手。

`NewChatRoom` 向 `App.OnShutdown` 注册清理：拒绝新用户、关闭连接、等待读写协程退出。运行示例请使用 `go run .`，同时编译消息处理和客户端写入模块。

浏览器每 20 秒发送应用心跳，pong 仅回复当前连接；页面退出会取消心跳与重连定时器。发送前检查连接状态和 64 KiB 的浏览器发送积压，页面最多保留 200 条消息。

## 技术特性

### 后端技术栈
- **Bingo框架**: 高性能Web框架
- **fasthttp**: 高性能HTTP服务器
- **bytedance/sonic**: 高性能JSON处理

### 前端技术栈
- **原生JavaScript**: 无框架依赖
- **WebSocket API**: 实时通信
- **CSS3**: 现代化样式和动画
- **HTML5**: 语义化标记

## 性能优势

### 1. 框架优势
- 使用fasthttp提供高性能HTTP服务
- 内置连接池和内存管理
- 优化的路由匹配算法
- 高效的中间件系统

### 2. WebSocket优化
- 连接复用和池化管理
- 消息序列化优化
- 内存使用优化
- 并发安全的消息广播

### 3. 前端优化
- 消息批量处理
- 自动重连机制
- 错误恢复能力
- 响应式设计

## 扩展建议

### 1. 功能扩展
- 用户认证和授权
- 多房间支持
- 文件传输功能
- 消息历史记录
- 表情符号支持

### 2. 性能扩展
- Redis消息队列
- 数据库持久化
- 负载均衡
- 集群部署

### 3. 监控扩展
- 性能指标监控
- 错误日志收集
- 用户行为分析
- 系统健康检查

## 测试方法

### 1. 功能测试
```bash
# 启动服务器
go run .

# 访问页面
curl http://localhost:8080

# 测试WebSocket端点
curl http://localhost:8080/ws
```

### 2. 压力测试
```bash
# 使用多个浏览器标签页
# 或使用WebSocket测试工具
```

### 3. 集成测试
- 测试用户加入/离开
- 测试消息广播
- 测试特殊命令
- 测试连接断开重连

## 故障排除

### 常见问题

1. **编译错误**
   - 确保Go版本 >= 1.26.6；推荐工具链为 Go 1.27.1
   - 检查依赖是否正确安装
   - 确认模块路径正确

2. **运行错误**
   - 检查端口是否被占用
   - 确认防火墙设置
   - 查看错误日志

3. **WebSocket连接失败**
   - 检查网络连接
   - 确认WebSocket协议支持
   - 查看浏览器控制台错误

### 调试技巧

1. **启用详细日志**
   - 框架日志级别在配置中调整：`config.LogLevel = "debug"`
   - 标准库 log 没有 SetLevel/DebugLevel API，请勿使用

2. **使用浏览器开发者工具**
   - Network标签查看WebSocket连接
   - Console标签查看错误信息

3. **使用测试工具**
   - WebSocket客户端工具
   - 网络抓包工具

## 贡献指南

欢迎提交Issue和Pull Request来改进这个示例！

### 开发流程

1. Fork项目
2. 创建功能分支
3. 提交代码
4. 创建Pull Request

### 代码规范

- 遵循Go代码规范
- 添加适当的注释
- 编写测试用例
- 更新文档

## 许可证

MIT License

## 相关链接

- [Bingo框架文档](../README.md)
- [WebSocket规范](https://tools.ietf.org/html/rfc6455)
- [Go WebSocket指南](https://golang.org/pkg/net/http/#example_FileServer)
