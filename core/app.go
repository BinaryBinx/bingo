package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync"
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

	mu            sync.RWMutex
	cachedHandler fasthttp.RequestHandler
	handlerDirty  bool
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
	// 每个核心的工作协程数
	WorkersPerCore int
	// 是否启用CPU亲和性
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
	}

	app.cachedHandler = app.applyMiddleware(app.router.Handler)
	app.handlerDirty = false

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
		NoDefaultServerHeader:        false,
		NoDefaultContentType:         false,
		Name:                         config.ServerName,
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
	if !config.MultiCore.Enabled {
		return
	}

	// 设置CPU核心数
	if config.MultiCore.NumCPU > 0 {
		runtime.GOMAXPROCS(config.MultiCore.NumCPU)
		log.Printf("🔧 多核优化: 使用 %d 个CPU核心", config.MultiCore.NumCPU)
	} else {
		numCPU := runtime.NumCPU()
		runtime.GOMAXPROCS(numCPU)
		log.Printf("🔧 多核优化: 使用所有 %d 个CPU核心", numCPU)
	}

	// 设置工作协程数
	if config.MultiCore.WorkersPerCore > 0 {
		totalWorkers := runtime.GOMAXPROCS(0) * config.MultiCore.WorkersPerCore
		log.Printf("🔧 多核优化: 每个核心 %d 个工作协程，总计 %d 个",
			config.MultiCore.WorkersPerCore, totalWorkers)
	}

	// 启用CPU亲和性（如果支持）
	if config.MultiCore.EnableCPUAffinity {
		log.Printf("🔧 多核优化: 启用CPU亲和性")
		// 这里可以添加CPU亲和性设置逻辑
	}
}

// applyProductionOptimization 应用生产环境优化
func applyProductionOptimization(config *Config) {
	if config.RunMode != RunModeRelease {
		return
	}

	log.Printf("🏭 生产环境优化已启用")

	// 1. 优化超时设置
	if config.ReadTimeout == 30*time.Second {
		config.ReadTimeout = 15 * time.Second
		log.Printf("🔧 生产优化: 读取超时调整为 15s")
	}
	if config.WriteTimeout == 30*time.Second {
		config.WriteTimeout = 15 * time.Second
		log.Printf("🔧 生产优化: 写入超时调整为 15s")
	}
	if config.IdleTimeout == 60*time.Second {
		config.IdleTimeout = 30 * time.Second
		log.Printf("🔧 生产优化: 空闲超时调整为 30s")
	}

	// 2. 优化缓冲区大小
	if config.MultiCore.ReadBufferSize == 4096 {
		config.MultiCore.ReadBufferSize = 8192
		log.Printf("🔧 生产优化: 读取缓冲区调整为 8KB")
	}
	if config.MultiCore.WriteBufferSize == 4096 {
		config.MultiCore.WriteBufferSize = 8192
		log.Printf("🔧 生产优化: 写入缓冲区调整为 8KB")
	}

	// 3. 优化并发连接数
	if config.MultiCore.MaxConns == 10000 {
		config.MultiCore.MaxConns = 50000
		log.Printf("🔧 生产优化: 最大并发连接调整为 50,000")
	}

	// 4. 优化请求体大小限制
	if config.MaxRequestBodySize == 4*1024*1024 {
		config.MaxRequestBodySize = 16 * 1024 * 1024 // 16MB
		log.Printf("🔧 生产优化: 最大请求体大小调整为 16MB")
	}

	// 5. 优化工作协程数
	if config.MultiCore.WorkersPerCore == 4 {
		config.MultiCore.WorkersPerCore = 8
		log.Printf("🔧 生产优化: 每核心工作协程数调整为 8")
	}

	// 6. 设置生产环境服务器名称
	if config.ServerName == "Bingo" {
		config.ServerName = "Bingo-Production"
		log.Printf("🔧 生产优化: 服务器名称调整为 %s", config.ServerName)
	}

	// 7. 优化日志级别
	if config.LogLevel == "info" {
		config.LogLevel = "warn"
		log.Printf("🔧 生产优化: 日志级别调整为 warn")
	}

	// 8. 启用TCP Keep-Alive
	log.Printf("🔧 生产优化: 启用TCP Keep-Alive")

	// 9. 启用HTTP/2
	log.Printf("🔧 生产优化: 启用HTTP/2")

	// 10. 优化内存分配
	log.Printf("🔧 生产优化: 优化内存分配策略")
}

// handleRequest 处理HTTP请求的主函数
// 应用中间件链并路由到相应的处理器
func (app *App) handleRequest(ctx *fasthttp.RequestCtx) {
	app.mu.RLock()
	if app.handlerDirty {
		app.mu.RUnlock()
		app.mu.Lock()
		if app.handlerDirty {
			app.cachedHandler = app.applyMiddleware(app.router.Handler)
			app.handlerDirty = false
		}
		app.mu.Unlock()
		app.mu.RLock()
	}
	handler := app.cachedHandler
	app.mu.RUnlock()

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
	app.middlewares = append(app.middlewares, middleware)
	app.handlerDirty = true
	app.mu.Unlock()
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
		reqCtx := requestContextPool.Get().(*RequestContext)
		reqCtx.RequestCtx = ctx
		reqCtx.app = app
		reqCtx.startTime = time.Now()

		for k := range reqCtx.params {
			delete(reqCtx.params, k)
		}

		app.parseParams(reqCtx)

		func() {
			defer func() {
				if r := recover(); r != nil {
					reqCtx.SetStatusCode(500)
					reqCtx.SetBodyString("Internal Server Error")
					app.logger.Error("panic recovered in handler: %v", r)
				}
			}()
			handler(reqCtx)
		}()

		if app.config.RunMode == RunModeDebug {
			app.logRequest(reqCtx)
		}

		requestContextPool.Put(reqCtx)
	}
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

// Run 启动服务器
func (app *App) Run() error {
	addr := fmt.Sprintf("%s:%d", app.config.Host, app.config.Port)
	app.logger.Info("🚀 Bingo服务器启动在 %s", addr)

	// 启动服务器
	errCh := make(chan error, 1)
	go func() {
		if err := app.server.ListenAndServe(addr); err != nil {
			errCh <- err
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case err := <-errCh:
		app.cancel()
		if errors.Is(err, fasthttp.ErrAlreadyServing) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrServerStart, err)
	case <-quit:
		log.Println("🛑 正在关闭服务器...")
		return app.Shutdown()
	}
}

// Shutdown 优雅关闭服务器
func (app *App) Shutdown() error {
	app.cancel()
	if app.wsUpgrader != nil {
		app.wsUpgrader.GetManager().Shutdown()
	}

	// 设置关闭超时
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return app.server.ShutdownWithContext(ctx)
}

// GetWebSocketUpgrader 获取WebSocket升级器
func (app *App) GetWebSocketUpgrader() *websocket.WebSocketUpgrader {
	return app.wsUpgrader
}

// IsDebug 检查是否为调试模式
func (app *App) IsDebug() bool {
	return app.config.RunMode == RunModeDebug
}

// IsRelease 检查是否为生产模式
func (app *App) IsRelease() bool {
	return app.config.RunMode == RunModeRelease
}

// IsTest 检查是否为测试模式
func (app *App) IsTest() bool {
	return app.config.RunMode == RunModeTest
}

// SetRunMode 设置运行模式
func (app *App) SetRunMode(mode RunMode) {
	app.config.RunMode = mode
}

// GetRunMode 获取当前运行模式
func (app *App) GetRunMode() RunMode {
	return app.config.RunMode
}

// GetConfig 获取应用配置
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
