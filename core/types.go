package core

import (
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
)

// RequestHandler 定义HTTP请求处理器接口
type RequestHandler func(*RequestContext)

// Middleware 定义中间件接口
type Middleware func(fasthttp.RequestHandler) fasthttp.RequestHandler

// MiddlewareFunc 定义中间件函数类型
type MiddlewareFunc func(*RequestContext) bool

// NewMiddleware 从MiddlewareFunc创建Middleware
func NewMiddleware(fn MiddlewareFunc) Middleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// 从对象池复用RequestContext，避免每次请求分配。
			// 注意：独立的中间件无法确定所属 App，acquire 后 app 为 nil，
			// ctx.App() 返回 nil，不会串用其他应用的旧实例
			reqCtx := acquireRequestContext()
			reqCtx.RequestCtx = ctx
			defer releaseRequestContext(reqCtx)

			// 执行中间件函数
			if fn(reqCtx) {
				// 如果中间件返回true，继续执行下一个处理器
				next(ctx)
			}
		}
	}
}

// acquireRequestContext 从对象池获取RequestContext并清理复用状态：
// 清空上一请求残留的路径参数，确保新请求拿到干净的实例
func acquireRequestContext() *RequestContext {
	reqCtx := requestContextPool.Get().(*RequestContext)
	if reqCtx.params == nil {
		reqCtx.params = make(map[string]string)
	} else if len(reqCtx.params) > 0 {
		clear(reqCtx.params)
	}
	return reqCtx
}

// releaseRequestContext 归还RequestContext到对象池：
// 清空持有的指针与时间戳，避免跨请求/跨应用引用串用；
// 路径参数过大的 map 直接丢弃，避免池中滞留大内存高水位
func releaseRequestContext(reqCtx *RequestContext) {
	reqCtx.RequestCtx = nil
	reqCtx.app = nil
	reqCtx.startTime = time.Time{}
	if len(reqCtx.params) > 128 {
		reqCtx.params = make(map[string]string, 8)
	} else if len(reqCtx.params) > 0 {
		clear(reqCtx.params)
	}
	requestContextPool.Put(reqCtx)
}

// resetErrorResponse 重建 500 错误响应：清理 handler 可能已写入的实体相关头
// （Content-Encoding/Content-Type/Content-Length 等），避免客户端按旧头解析
// 错误正文导致解码失败。保留请求追踪（X-Request-ID）与跨域头
func resetErrorResponse(ctx *fasthttp.RequestCtx) {
	h := &ctx.Response.Header
	h.Del("Content-Encoding")
	h.Del("Content-Type")
	h.Del("Content-Length")
	h.Del("Transfer-Encoding")
	h.Del("Content-Range")
	h.Del("ETag")
	h.Del("Last-Modified")
	ctx.Response.ResetBody()
	ctx.SetContentType("text/plain; charset=utf-8")
	ctx.SetStatusCode(fasthttp.StatusInternalServerError)
	ctx.SetBodyString("Internal Server Error")
}

// RequestContext 扩展fasthttp.RequestCtx，提供更友好的API
type RequestContext struct {
	*fasthttp.RequestCtx
	app       *App
	params    map[string]string
	startTime time.Time
}

// SetApp 设置关联的应用实例
func (c *RequestContext) SetApp(app *App) {
	c.app = app
}

// App 获取关联的应用实例
func (c *RequestContext) App() *App {
	return c.app
}

// GetParam 获取路径参数
func (c *RequestContext) GetParam(key string) string {
	return c.params[key]
}

// SetParam 设置路径参数
func (c *RequestContext) SetParam(key, value string) {
	c.params[key] = value
}

// JSON 发送JSON响应
func (c *RequestContext) JSON(statusCode int, data interface{}) error {
	c.SetStatusCode(statusCode)
	c.SetContentType("application/json")

	// 使用sonic进行JSON序列化
	jsonData, err := sonic.Marshal(data)
	if err != nil {
		return err
	}

	c.SetBody(jsonData)
	return nil
}

// BindJSON 绑定JSON请求体到结构体
func (c *RequestContext) BindJSON(v interface{}) error {
	return sonic.Unmarshal(c.PostBody(), v)
}

// String 发送字符串响应
func (c *RequestContext) String(statusCode int, format string, args ...interface{}) {
	c.SetStatusCode(statusCode)
	c.SetContentType("text/plain; charset=utf-8")
	if len(args) == 0 {
		// 无参数时直接写入，避免 fmt.Sprintf 的反射开销和分配
		c.SetBodyString(format)
		return
	}
	c.SetBodyString(fmt.Sprintf(format, args...))
}

// HTML 发送HTML响应
func (c *RequestContext) HTML(statusCode int, html string) {
	c.SetStatusCode(statusCode)
	c.SetContentType("text/html; charset=utf-8")
	c.SetBodyString(html)
}

// Redirect 重定向
func (c *RequestContext) Redirect(statusCode int, url string) {
	c.Response.Header.Set("Location", url)
	c.SetStatusCode(statusCode)
}

// GetHeader 获取请求头
func (c *RequestContext) GetHeader(key string) string {
	return string(c.Request.Header.Peek(key))
}

// SetHeader 设置响应头
func (c *RequestContext) SetHeader(key, value string) {
	c.Response.Header.Set(key, value)
}

// GetQuery 获取查询参数
func (c *RequestContext) GetQuery(key string) string {
	return string(c.QueryArgs().Peek(key))
}

// GetForm 获取表单参数
func (c *RequestContext) GetForm(key string) string {
	return string(c.FormValue(key))
}

// RouterGroup 路由组，用于组织相关路由
type RouterGroup struct {
	app    *App
	prefix string
}

// GET 在路由组中注册GET路由
func (g *RouterGroup) GET(path string, handler RequestHandler) {
	g.app.router.GET(g.prefix+path, g.app.wrapHandler(handler))
}

// POST 在路由组中注册POST路由
func (g *RouterGroup) POST(path string, handler RequestHandler) {
	g.app.router.POST(g.prefix+path, g.app.wrapHandler(handler))
}

// PUT 在路由组中注册PUT路由
func (g *RouterGroup) PUT(path string, handler RequestHandler) {
	g.app.router.PUT(g.prefix+path, g.app.wrapHandler(handler))
}

// DELETE 在路由组中注册DELETE路由
func (g *RouterGroup) DELETE(path string, handler RequestHandler) {
	g.app.router.DELETE(g.prefix+path, g.app.wrapHandler(handler))
}

// PATCH 在路由组中注册PATCH路由
func (g *RouterGroup) PATCH(path string, handler RequestHandler) {
	g.app.router.PATCH(g.prefix+path, g.app.wrapHandler(handler))
}

// HEAD 在路由组中注册HEAD路由
func (g *RouterGroup) HEAD(path string, handler RequestHandler) {
	g.app.router.HEAD(g.prefix+path, g.app.wrapHandler(handler))
}

// OPTIONS 在路由组中注册OPTIONS路由
func (g *RouterGroup) OPTIONS(path string, handler RequestHandler) {
	g.app.router.OPTIONS(g.prefix+path, g.app.wrapHandler(handler))
}

// Group 创建子路由组
func (g *RouterGroup) Group(prefix string) *RouterGroup {
	return &RouterGroup{
		app:    g.app,
		prefix: g.prefix + prefix,
	}
}
