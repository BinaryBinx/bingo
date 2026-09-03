package core

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// TestNewApp 测试创建应用实例
func TestNewApp(t *testing.T) {
	config := DefaultConfig()
	app := NewApp(config)

	if app == nil {
		t.Error("NewApp should return a non-nil App instance")
	}

	if app.config != config {
		t.Error("App should use the provided config")
	}

	if app.router == nil {
		t.Error("App should have a router")
	}

	if app.server == nil {
		t.Error("App should have a server")
	}
}

// TestAppGET 测试GET路由
func TestAppGET(t *testing.T) {
	app := NewApp(nil)

	// 注册GET路由
	app.GET("/test", func(ctx *RequestContext) {
		ctx.String(http.StatusOK, "Hello, World!")
	})

	// 转换为fasthttp请求
	fasthttpReq := &fasthttp.RequestCtx{}
	fasthttpReq.Request.Header.SetMethod("GET")
	fasthttpReq.Request.SetRequestURI("/test")

	// 处理请求
	app.handleRequest(fasthttpReq)

	// 检查响应
	if fasthttpReq.Response.StatusCode() != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, fasthttpReq.Response.StatusCode())
	}

	expectedBody := "Hello, World!"
	if string(fasthttpReq.Response.Body()) != expectedBody {
		t.Errorf("Expected body %s, got %s", expectedBody, string(fasthttpReq.Response.Body()))
	}
}

// TestAppMiddleware 测试中间件
func TestAppMiddleware(t *testing.T) {
	app := NewApp(nil)

	// 注册中间件
	app.Use(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.Response.Header.Set("X-Test", "test")
			next(ctx)
		}
	})

	// 注册GET路由
	app.GET("/test", func(ctx *RequestContext) {
		ctx.String(http.StatusOK, "Hello, World!")
	})

	// 创建测试请求
	fasthttpReq := &fasthttp.RequestCtx{}
	fasthttpReq.Request.Header.SetMethod("GET")
	fasthttpReq.Request.SetRequestURI("/test")

	// 处理请求
	app.handleRequest(fasthttpReq)

	// 检查响应
	if fasthttpReq.Response.StatusCode() != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, fasthttpReq.Response.StatusCode())
	}

	if string(fasthttpReq.Response.Header.Peek("X-Test")) != "test" {
		t.Error("Middleware should set X-Test header")
	}
}

// TestAppRunMode 测试运行模式
func TestAppRunMode(t *testing.T) {
	app := NewApp(nil)

	// 测试默认运行模式
	if app.GetRunMode() != RunModeDebug {
		t.Errorf("Expected default run mode %s, got %s", RunModeDebug, app.GetRunMode())
	}

	// 测试设置运行模式
	app.SetRunMode(RunModeRelease)
	if app.GetRunMode() != RunModeRelease {
		t.Errorf("Expected run mode %s, got %s", RunModeRelease, app.GetRunMode())
	}

	// 测试运行模式检查方法
	if !app.IsRelease() {
		t.Error("IsRelease should return true when run mode is release")
	}

	if app.IsDebug() {
		t.Error("IsDebug should return false when run mode is release")
	}

	if app.IsTest() {
		t.Error("IsTest should return false when run mode is release")
	}
}

// TestConfig 测试配置
func TestConfig(t *testing.T) {
	config := DefaultConfig()

	if config.Host != "0.0.0.0" {
		t.Errorf("Expected default host %s, got %s", "0.0.0.0", config.Host)
	}

	if config.Port != 8080 {
		t.Errorf("Expected default port %d, got %d", 8080, config.Port)
	}

	if config.ReadTimeout != 30*time.Second {
		t.Errorf("Expected default read timeout %v, got %v", 30*time.Second, config.ReadTimeout)
	}

	if config.WriteTimeout != 30*time.Second {
		t.Errorf("Expected default write timeout %v, got %v", 30*time.Second, config.WriteTimeout)
	}

	if config.IdleTimeout != 60*time.Second {
		t.Errorf("Expected default idle timeout %v, got %v", 60*time.Second, config.IdleTimeout)
	}

	if config.MaxRequestBodySize != 4*1024*1024 {
		t.Errorf("Expected default max request body size %d, got %d", 4*1024*1024, config.MaxRequestBodySize)
	}

	if config.ServerName != "Bingo" {
		t.Errorf("Expected default server name %s, got %s", "Bingo", config.ServerName)
	}

	if config.RunMode != RunModeDebug {
		t.Errorf("Expected default run mode %s, got %s", RunModeDebug, config.RunMode)
	}

	if config.LogLevel != "info" {
		t.Errorf("Expected default log level %s, got %s", "info", config.LogLevel)
	}
}

func TestNewAppNormalizesInvalidConfig(t *testing.T) {
	config := &Config{
		Host:               "",
		Port:               -1,
		ReadTimeout:        -1,
		WriteTimeout:       -1,
		IdleTimeout:        -1,
		MaxRequestBodySize: 0,
		RunMode:            RunMode("invalid"),
		LogLevel:           "nope",
		MultiCore: MultiCoreConfig{
			Enabled:         true,
			NumCPU:          -1,
			WorkersPerCore:  0,
			MaxConns:        -10,
			ReadBufferSize:  0,
			WriteBufferSize: 0,
		},
	}

	app := NewApp(config)
	defaults := DefaultConfig()

	if app.GetConfig().Host != defaults.Host {
		t.Errorf("Expected normalized host %s, got %s", defaults.Host, app.GetConfig().Host)
	}
	if app.GetConfig().Port != defaults.Port {
		t.Errorf("Expected normalized port %d, got %d", defaults.Port, app.GetConfig().Port)
	}
	if app.GetConfig().ReadTimeout != defaults.ReadTimeout {
		t.Errorf("Expected normalized read timeout %v, got %v", defaults.ReadTimeout, app.GetConfig().ReadTimeout)
	}
	if app.GetRunMode() != defaults.RunMode {
		t.Errorf("Expected normalized run mode %s, got %s", defaults.RunMode, app.GetRunMode())
	}
	if app.GetLogLevel() != defaults.LogLevel {
		t.Errorf("Expected normalized log level %s, got %s", defaults.LogLevel, app.GetLogLevel())
	}
	if app.GetMultiCoreConfig().MaxConns != defaults.MultiCore.MaxConns {
		t.Errorf("Expected normalized max conns %d, got %d", defaults.MultiCore.MaxConns, app.GetMultiCoreConfig().MaxConns)
	}
}

// TestRunModeConcurrentAccess 并发 Set/GetRunMode 不得产生 data race（配合 -race 验证）
func TestRunModeConcurrentAccess(t *testing.T) {
	app := NewApp(nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				_ = app.GetRunMode()
				_ = app.IsDebug()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				app.SetRunMode(RunModeRelease)
			}
		}()
	}
	wg.Wait()
}

// TestServeAndExternalShutdown Serve 在外部 Shutdown 后应返回
func TestServeAndExternalShutdown(t *testing.T) {
	app := NewApp(nil)
	app.SetRunMode(RunModeRelease)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- app.Serve(ln)
	}()

	// 发真实请求确认服务可用
	deadline := time.Now().Add(3 * time.Second)
	var resp *http.Response
	var reqErr error
	for time.Now().Before(deadline) {
		resp, reqErr = http.Get("http://" + ln.Addr().String() + "/")
		if reqErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reqErr != nil {
		t.Fatalf("request failed: %v", reqErr)
	}
	resp.Body.Close()

	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}

// TestRunReturnsAfterExternalShutdown 外部调用 Shutdown 后，Run 应正常返回
func TestRunReturnsAfterExternalShutdown(t *testing.T) {
	// 探测空闲端口
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	config := DefaultConfig()
	config.Host = "127.0.0.1"
	config.Port = port
	config.RunMode = RunModeRelease
	app := NewApp(config)

	runDone := make(chan error, 1)
	go func() {
		runDone <- app.Run()
	}()

	// 等待服务器启动后外部触发关闭
	deadline := time.Now().Add(3 * time.Second)
	var clientErr error
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	for time.Now().Before(deadline) {
		var resp *http.Response
		resp, clientErr = http.Get(url)
		if clientErr == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if clientErr != nil {
		// 无法连通时仍尝试关闭，避免遗留进程
		_ = app.Shutdown()
		t.Fatalf("server did not start: %v", clientErr)
	}

	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after external Shutdown")
	}
}
