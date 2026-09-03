package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/BinaryBinx/bingo/websocket"

	"github.com/fasthttp/router"
	"github.com/valyala/fasthttp"
)

// RunMode 运行模式
type RunMode string

const (
	// RunModeDebug 调试模式
	RunModeDebug RunMode = "debug"
	// RunModeRelease 生产模式
	RunModeRelease RunMode = "release"
	// RunModeTest 测试模式
	RunModeTest RunMode = "test"
)

// App 是Bingo框架的核心应用结构
// 负责管理HTTP服务器、路由、中间件和WebSocket连接
type App struct {
	server      *fasthttp.Server
	router      *router.Router
	config      *Config
	middlewares []Middleware
	wsUpgrader  *websocket.WebSocketUpgrader
	ctx         context.Context
	cancel      context.CancelFunc
	logger      *Logger

	// mu 保护中间件切片的读写
	mu sync.Mutex
	// handler 是中间件链应用后的最终处理器，读取频率极高（每请求），
	// 使用 atomic.Pointer 避免每请求加锁
	handler atomic.Pointer[fasthttp.RequestHandler]
	// runMode 原子保存运行模式，避免请求处理路径与 Set/GetRunMode 并发读写 data race
	runMode atomic.Pointer[RunMode]
	// shutdownCh 在优雅关闭完成时关闭，用于通知 Run 返回
	shutdownCh chan struct{}
	// shutdownOnce 保证 shutdownCh 只关闭一次
	shutdownOnce sync.Once
}

// Config 应用配置
type Config struct {
	// 基础配置
	Host               string        `json:"host"`                  // 监听地址
	Port               int           `json:"port"`                  // 监听端口
	ReadTimeout        time.Duration `json:"read_timeout"`          // 读取超时
	WriteTimeout       time.Duration `json:"write_timeout"`         // 写入超时
	IdleTimeout        time.Duration `json:"idle_timeout"`          // 空闲超时
	MaxRequestBodySize int           `json:"max_request_body_size"` // 最大请求体大小
	ServerName         string        `json:"server_name"`           // 服务器名称
	RunMode            RunMode       `json:"run_mode"`              // 运行模式
	LogLevel           string        `json:"log_level"`             // 日志级别

	// 多核性能配置
	MultiCore MultiCoreConfig `json:"multi_core"`
}

// MultiCoreConfig 多核性能配置
type MultiCoreConfig struct {
	// 是否启用多核优化
	Enabled bool
	// 指定使用的CPU核心数（0表示使用所有核心）
	NumCPU int
	// 每个核心的工作协程数（预留字段，fasthttp 自带 worker pool，当前版本不生效）
	WorkersPerCore int
	// 是否启用CPU亲和性（预留字段，当前版本不生效）
	EnableCPUAffinity bool
	// 最大并发连接数
	MaxConns int
	// 连接缓冲区大小
	ReadBufferSize  int
	WriteBufferSize int
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Host:               "0.0.0.0",
		Port:               8080,
		ReadTimeout:        30 * time.Second,
		WriteTimeout:       30 * time.Second,
		IdleTimeout:        60 * time.Second,
		MaxRequestBodySize: 4 * 1024 * 1024, // 4MB
		ServerName:         "Bingo",
		RunMode:            RunModeDebug,
		LogLevel:           "info",
		MultiCore: MultiCoreConfig{
			Enabled:         true,
			NumCPU:          0, // 0表示使用所有CPU核心
			WorkersPerCore:  4,
			MaxConns:        10000,
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
}

// NewApp 创建新的应用实例
func NewApp(config *Config) *App {
	if config == nil {
		config = DefaultConfig()
	}

	normalizeConfig(config)

	// 应用多核性能优化
	applyMultiCoreOptimization(config)

	// 应用生产环境优化
	applyProductionOptimization(config)

	ctx, cancel := context.WithCancel(context.Background())

	// 初始化日志记录器
	logLevel := GetLogLevelFromName(config.LogLevel)
	logger := NewLogger(logLevel, "Bingo")

	app := &App{
		router:      router.New(),
		config:      config,
		middlewares: make([]Middleware, 0),
		ctx:         ctx,
		cancel:      cancel,
		logger:      logger,
		shutdownCh:  make(chan struct{}),
	}
	mode := config.RunMode
	app.runMode.Store(&mode)

	handler := app.applyMiddleware(app.router.Handler)
	app.handler.Store(&handler)

	// 初始化WebSocket升级器
	app.wsUpgrader = websocket.NewWebSocketUpgrader(nil)

	// 配置fasthttp服务器
	app.server = &fasthttp.Server{
		Handler:                      app.handleRequest,
		ReadTimeout:                  config.ReadTimeout,
		WriteTimeout:                 config.WriteTimeout,
		IdleTimeout:                  config.IdleTimeout,
		MaxRequestBodySize:           config.MaxRequestBodySize,
		DisablePreParseMultipartForm: true,
		GetOnly:                      false,
		NoDefaultServerHeader:        true,
		NoDefaultContentType:         false,
		Name:                         config.ServerName,
		TCPKeepalive:                 true,
		TCPKeepalivePeriod:           30 * time.Second, // 定期探测死连接，及时清理失效连接
		ReduceMemoryUsage:            true,
		// 多核性能优化配置
		Concurrency:     config.MultiCore.MaxConns,
		ReadBufferSize:  config.MultiCore.ReadBufferSize,
		WriteBufferSize: config.MultiCore.WriteBufferSize,
	}

	return app
}

// normalizeConfig 修正不安全或无效的配置值，避免启动时出现隐式 panic 或不可预测行为。
func normalizeConfig(config *Config) {
	defaults := DefaultConfig()

	if config.Host == "" {
		config.Host = defaults.Host
	}
	if config.Port <= 0 || config.Port > 65535 {
		config.Port = defaults.Port
	}
	if config.ReadTimeout < 0 {
		config.ReadTimeout = defaults.ReadTimeout
	}
	if config.WriteTimeout < 0 {
		config.WriteTimeout = defaults.WriteTimeout
	}
	if config.IdleTimeout < 0 {
		config.IdleTimeout = defaults.IdleTimeout
	}
	if config.MaxRequestBodySize <= 0 {
		config.MaxRequestBodySize = defaults.MaxRequestBodySize
	}
	if config.ServerName == "" {
		config.ServerName = defaults.ServerName
	}
	switch config.RunMode {
	case RunModeDebug, RunModeRelease, RunModeTest:
	default:
		config.RunMode = defaults.RunMode
	}
	if !isKnownLogLevel(config.LogLevel) {
		config.LogLevel = defaults.LogLevel
	}

	if config.MultiCore.NumCPU < 0 {
		config.MultiCore.NumCPU = defaults.MultiCore.NumCPU
	}
	if config.MultiCore.WorkersPerCore <= 0 {
		config.MultiCore.WorkersPerCore = defaults.MultiCore.WorkersPerCore
	}
	if config.MultiCore.MaxConns <= 0 {
		config.MultiCore.MaxConns = defaults.MultiCore.MaxConns
	}
	if config.MultiCore.ReadBufferSize <= 0 {
		config.MultiCore.ReadBufferSize = defaults.MultiCore.ReadBufferSize
	}
	if config.MultiCore.WriteBufferSize <= 0 {
		config.MultiCore.WriteBufferSize = defaults.MultiCore.WriteBufferSize
	}
}

func isKnownLogLevel(name string) bool {
	switch name {
	case "debug", "info", "warn", "error", "fatal":
		return true
	default:
		return false
	}
}

// applyMultiCoreOptimization 应用多核性能优化
func applyMultiCoreOptimization(config *Config) {
	logf := func(format string, args ...interface{}) {
		if config.RunMode == RunModeDebug {
			log.Printf(format, args...)
		}
	}

	if !config.MultiCore.Enabled {
		return
	}

	// 仅在显式指定正数时覆盖 GOMAXPROCS；NumCPU==0 保留运行时现有调度策略
	// （Go 1.21+ 会按 Linux cgroup 配额自动调整，显式调用反而会关闭自动更新）
	if config.MultiCore.NumCPU > 0 {
		runtime.GOMAXPROCS(config.MultiCore.NumCPU)
		logf("🔧 多核优化: 使用 %d 个CPU核心", config.MultiCore.NumCPU)
	} else {
		logf("🔧 多核优化: 保留运行时现有核心调度策略")
	}

	// 注意：fasthttp 内部自带 worker pool，WorkersPerCore 与 CPU 亲和性
	// 配置项为预留字段，当前版本暂不生效（避免输出误导性日志）
}

// applyProductionOptimization 应用生产环境优化
func applyProductionOptimization(config *Config) {
	logf := func(format string, args ...interface{}) {
		if config.RunMode == RunModeDebug {
			log.Printf(format, args...)
		}
	}

	if config.RunMode != RunModeRelease {
		return
	}

	logf("🏭 生产环境优化已启用")

	// 1. 优化超时设置
	if config.ReadTimeout == 30*time.Second {
		config.ReadTimeout = 15 * time.Second
		logf("🔧 生产优化: 读取超时调整为 15s")
	}
	if config.WriteTimeout == 30*time.Second {
		config.WriteTimeout = 15 * time.Second
		logf("🔧 生产优化: 写入超时调整为 15s")
	}
	if config.IdleTimeout == 60*time.Second {
		config.IdleTimeout = 30 * time.Second
		logf("🔧 生产优化: 空闲超时调整为 30s")
	}

	// 2. 优化缓冲区大小
	if config.MultiCore.ReadBufferSize == 4096 {
		config.MultiCore.ReadBufferSize = 8192
		logf("🔧 生产优化: 读取缓冲区调整为 8KB")
	}
	if config.MultiCore.WriteBufferSize == 4096 {
		config.MultiCore.WriteBufferSize = 8192
		logf("🔧 生产优化: 写入缓冲区调整为 8KB")
	}

	// 3. 优化并发连接数
	if config.MultiCore.MaxConns == 10000 {
		config.MultiCore.MaxConns = 50000
		logf("🔧 生产优化: 最大并发连接调整为 50,000")
	}

	// 4. 优化请求体大小限制
	if config.MaxRequestBodySize == 4*1024*1024 {
		config.MaxRequestBodySize = 16 * 1024 * 1024 // 16MB
		logf("🔧 生产优化: 最大请求体大小调整为 16MB")
	}

	// 5. 优化工作协程数
	if config.MultiCore.WorkersPerCore == 4 {
		config.MultiCore.WorkersPerCore = 8
		logf("🔧 生产优化: 每核心工作协程数调整为 8")
	}

	// 6. 设置生产环境服务器名称
	if config.ServerName == "Bingo" {
		config.ServerName = "Bingo-Production"
		logf("🔧 生产优化: 服务器名称调整为 %s", config.ServerName)
	}

	// 7. 优化日志级别
	if config.LogLevel == "info" {
		config.LogLevel = "warn"
		logf("🔧 生产优化: 日志级别调整为 warn")
	}

	// 8. 启用TCP Keep-Alive（在 NewApp 中通过 fasthttp.Server.TCPKeepalive 启用）
	logf("🔧 生产优化: 启用TCP Keep-Alive")

	// 9. 优化内存分配策略
	logf("🔧 生产优化: 优化内存分配策略")
}

// handleRequest 处理HTTP请求的主函数
// 应用中间件链并路由到相应的处理器
func (app *App) handleRequest(ctx *fasthttp.RequestCtx) {
	handler := *app.handler.Load()
	handler(ctx)
}

// applyMiddleware 应用中间件链
func (app *App) applyMiddleware(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
	for i := len(app.middlewares) - 1; i >= 0; i-- {
		handler = app.middlewares[i](handler)
	}
	return handler
}

// Use 添加中间件到应用
func (app *App) Use(middleware Middleware) {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.middlewares = append(app.middlewares, middleware)
	// 中间件链变化后立即重建处理器，避免请求时再检查
	handler := app.applyMiddleware(app.router.Handler)
	app.handler.Store(&handler)
}

// GET 注册GET路由
func (app *App) GET(path string, handler RequestHandler) {
	app.router.GET(path, app.wrapHandler(handler))
}

// POST 注册POST路由
func (app *App) POST(path string, handler RequestHandler) {
	app.router.POST(path, app.wrapHandler(handler))
}

// PUT 注册PUT路由
func (app *App) PUT(path string, handler RequestHandler) {
	app.router.PUT(path, app.wrapHandler(handler))
}

// DELETE 注册DELETE路由
func (app *App) DELETE(path string, handler RequestHandler) {
	app.router.DELETE(path, app.wrapHandler(handler))
}

// PATCH 注册PATCH路由
func (app *App) PATCH(path string, handler RequestHandler) {
	app.router.PATCH(path, app.wrapHandler(handler))
}

// HEAD 注册HEAD路由
func (app *App) HEAD(path string, handler RequestHandler) {
	app.router.HEAD(path, app.wrapHandler(handler))
}

// OPTIONS 注册OPTIONS路由
func (app *App) OPTIONS(path string, handler RequestHandler) {
	app.router.OPTIONS(path, app.wrapHandler(handler))
}

// Group 创建路由组
func (app *App) Group(prefix string) *RouterGroup {
	return &RouterGroup{
		app:    app,
		prefix: prefix,
	}
}

var requestContextPool = sync.Pool{
	New: func() interface{} {
		return &RequestContext{
			params: make(map[string]string),
		}
	},
}

// wrapHandler 包装请求处理器
func (app *App) wrapHandler(handler RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		reqCtx := acquireRequestContext()
		reqCtx.RequestCtx = ctx
		reqCtx.app = app
		// 仅在需要记录请求日志时记录起始时间，避免 release 模式下无谓开销
		if app.currentRunMode() == RunModeDebug {
			reqCtx.startTime = time.Now()
		}

		app.parseParams(reqCtx)

		func() {
			defer func() {
				if r := recover(); r != nil {
					// 重建 500 响应并清理旧实体相关头，避免客户端按残留的
					// Content-Encoding/Content-Type 解码错误正文
					resetErrorResponse(ctx)
					app.logger.Error("panic recovered in handler: %v\n%s", r, debug.Stack())
				}
			}()
			handler(reqCtx)
		}()

		if app.currentRunMode() == RunModeDebug {
			app.logRequest(reqCtx)
		}

		releaseRequestContext(reqCtx)
	}
}

// currentRunMode 原子读取当前运行模式
func (app *App) currentRunMode() RunMode {
	if m := app.runMode.Load(); m != nil {
		return *m
	}
	return app.config.RunMode
}

// parseParams 解析URL参数
func (app *App) parseParams(reqCtx *RequestContext) {
	if ctx := reqCtx.RequestCtx; ctx != nil {
		ctx.VisitUserValues(func(key []byte, value interface{}) {
			switch v := value.(type) {
			case string:
				reqCtx.params[string(key)] = v
			case []byte:
				reqCtx.params[string(key)] = string(v)
			}
		})
	}
}

// logRequest 记录请求日志
func (app *App) logRequest(reqCtx *RequestContext) {
	duration := time.Since(reqCtx.startTime)
	app.logger.Info("%s %s %s - %d - %v",
		reqCtx.Method(),
		reqCtx.RequestURI(),
		reqCtx.RemoteAddr(),
		reqCtx.Response.StatusCode(),
		duration,
	)
}

// Run 启动服务器并阻塞，直到收到中断信号或外部调用 Shutdown 完成。
// 使用 net.Listen 自建监听器（net.JoinHostPort 正确处理 IPv6 地址），
// 再交给 fasthttp Serve，避免 ListenAndServe 固定使用 tcp4
func (app *App) Run() error {
	addr := net.JoinHostPort(app.config.Host, strconv.Itoa(app.config.Port))
	app.logger.Info("🚀 Bingo服务器启动在 %s", addr)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// 启动失败：完整回收与 App 生命周期绑定的后台资源（WebSocket 清理协程等）
		app.release()
		return fmt.Errorf("%w: %v", ErrServerStart, err)
	}

	// 服务器退出（正常关闭或出错）都无条件上报，供 Run 判断退出时机
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- app.Serve(ln)
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case err := <-serveErrCh:
		// Serve 已退出且未发生显式关闭，说明启动或运行出错
		app.release()
		if err == nil || errors.Is(err, fasthttp.ErrAlreadyServing) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrServerStart, err)
	case <-quit:
		log.Println("🛑 正在关闭服务器...")
		return app.Shutdown()
	case <-app.shutdownCh:
		// 外部调用 Shutdown 已完成：等待 Serve 退出后返回
		<-serveErrCh
		return nil
	}
}

// Shutdown 优雅关闭服务器。
// 整个退出流程共用统一的 30 秒期限：先以有限并发关闭 WebSocket 连接
// （到期强制回收），再执行 HTTP 服务器优雅关闭；完成后关闭通知通道，
// 让外部触发的 Run 正常返回
func (app *App) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app.cancel()
	if app.wsUpgrader != nil {
		app.wsUpgrader.GetManager().Shutdown(ctx)
	}

	err := app.server.ShutdownWithContext(ctx)

	app.shutdownOnce.Do(func() {
		close(app.shutdownCh)
	})
	return err
}

// Serve 在已创建的监听器上提供 HTTP 服务。
// 这是 Run 内部使用的底层方法，测试或其他需要自行管理监听器的场景也可直接调用。
// Serve 是阻塞的，直到 listener 被关闭或发生不可恢复的错误。
func (app *App) Serve(ln net.Listener) error {
	return app.server.Serve(ln)
}

// release 回收与 App 生命周期绑定的后台资源（WebSocket 清理协程等）。
// 用于启动失败等未走完整关闭流程的路径
func (app *App) release() {
	app.cancel()
	if app.wsUpgrader != nil {
		app.wsUpgrader.GetManager().Shutdown(context.Background())
	}
}

// GetWebSocketUpgrader 获取WebSocket升级器
func (app *App) GetWebSocketUpgrader() *websocket.WebSocketUpgrader {
	return app.wsUpgrader
}

// IsDebug 检查是否为调试模式
func (app *App) IsDebug() bool {
	return app.currentRunMode() == RunModeDebug
}

// IsRelease 检查是否为生产模式
func (app *App) IsRelease() bool {
	return app.currentRunMode() == RunModeRelease
}

// IsTest 检查是否为测试模式
func (app *App) IsTest() bool {
	return app.currentRunMode() == RunModeTest
}

// SetRunMode 设置运行模式。
// 运行期模式变更通过原子指针生效；config 结构视为启动前配置，
// 运行期修改请走本方法
func (app *App) SetRunMode(mode RunMode) {
	app.runMode.Store(&mode)
}

// GetRunMode 获取当前运行模式
func (app *App) GetRunMode() RunMode {
	return app.currentRunMode()
}

// GetConfig 获取应用配置。
// 返回的指针仅建议在启动前读取或修改；运行期修改请使用 Set/GetRunMode 等专用方法
func (app *App) GetConfig() *Config {
	return app.config
}

// GetServerName 获取服务器名称
func (app *App) GetServerName() string {
	return app.config.ServerName
}

// GetLogLevel 获取日志级别
func (app *App) GetLogLevel() string {
	return app.config.LogLevel
}

// GetReadTimeout 获取读取超时
func (app *App) GetReadTimeout() time.Duration {
	return app.config.ReadTimeout
}

// GetWriteTimeout 获取写入超时
func (app *App) GetWriteTimeout() time.Duration {
	return app.config.WriteTimeout
}

// GetIdleTimeout 获取空闲超时
func (app *App) GetIdleTimeout() time.Duration {
	return app.config.IdleTimeout
}

// GetMaxRequestBodySize 获取最大请求体大小
func (app *App) GetMaxRequestBodySize() int {
	return app.config.MaxRequestBodySize
}

// GetMultiCoreConfig 获取多核配置
func (app *App) GetMultiCoreConfig() MultiCoreConfig {
	return app.config.MultiCore
}
