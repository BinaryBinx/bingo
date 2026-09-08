package core

import (
	"context"
	"log"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
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
	shutdownCh    chan struct{}
	lifecycleMu   sync.Mutex
	started       bool
	shuttingDown  bool
	serveDone     chan struct{}
	serveErr      error
	shutdownErr   error
	shutdownHooks []func(context.Context) error
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
	JSONCopyStrings    bool          `json:"json_copy_strings"`     // 避免长期保存解码字段时引用整个请求体
	ReduceMemoryUsage  bool          `json:"reduce_memory_usage"`

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
		JSONCopyStrings:    true,
		ReduceMemoryUsage:  true,
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
	// Keep an immutable effective configuration; callers retain ownership of theirs.
	copyConfig := *config
	config = &copyConfig

	normalizeConfig(config)

	// 应用多核性能优化
	applyMultiCoreOptimization(config)

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
		serveDone:   make(chan struct{}),
	}
	mode := config.RunMode
	app.runMode.Store(&mode)

	handler := app.applyMiddleware(app.router.Handler)
	app.handler.Store(&handler)

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
		ReduceMemoryUsage:            config.ReduceMemoryUsage,
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
	// （现代 Go 会考虑 Linux cgroup 配额，显式调用会关闭自动更新）
	if config.MultiCore.NumCPU > 0 {
		runtime.GOMAXPROCS(config.MultiCore.NumCPU)
		logf("🔧 多核优化: 使用 %d 个CPU核心", config.MultiCore.NumCPU)
	} else {
		logf("🔧 多核优化: 保留运行时现有核心调度策略")
	}

	// 注意：fasthttp 内部自带 worker pool，WorkersPerCore 与 CPU 亲和性
	// 配置项为预留字段，当前版本暂不生效（避免输出误导性日志）
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
// 返回生效配置的独立快照。请在 NewApp 前配置资源限制；运行模式用 SetRunMode 修改。
func (app *App) GetConfig() *Config {
	config := *app.config
	config.RunMode = app.currentRunMode()
	return &config
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
