package middleware

import (
	"bytes"
	"context"
	"io"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/klauspost/compress/gzip"
	"github.com/valyala/fasthttp"
)

// Logger 日志中间件
func Logger() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			start := time.Now()

			// 执行下一个处理器
			next(ctx)

			// 记录请求日志
			duration := time.Since(start)
			log.Printf("[%s] %s %s - %d - %v",
				ctx.Method(),
				ctx.RequestURI(),
				ctx.RemoteAddr(),
				ctx.Response.StatusCode(),
				duration,
			)
		}
	}
}

// CORS 跨域中间件
func CORS(allowedOrigins []string, allowedMethods []string, allowedHeaders []string) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	// 预计算方法与头信息，避免每次请求重复 strings.Join
	allowAllOrigins := false
	for _, o := range allowedOrigins {
		if o == "*" {
			allowAllOrigins = true
			break
		}
	}

	originAllowed := func(origin string) bool {
		for _, allowedOrigin := range allowedOrigins {
			if allowedOrigin == "*" || allowedOrigin == origin {
				return true
			}
		}
		return false
	}

	// 提前预计算响应头值
	methodsValue := "GET, POST, PUT, DELETE, OPTIONS"
	if len(allowedMethods) > 0 {
		methodsValue = strings.Join(allowedMethods, ", ")
	}
	headersValue := "Content-Type, Authorization"
	if len(allowedHeaders) > 0 {
		headersValue = strings.Join(allowedHeaders, ", ")
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowCredentials := false

			switch {
			case len(allowedOrigins) == 0:
				ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
			case allowAllOrigins:
				ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
			case origin != "" && originAllowed(origin):
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				addVary(&ctx.Response.Header, "Origin")
				allowCredentials = true
			}

			// 设置其他CORS头
			ctx.Response.Header.Set("Access-Control-Allow-Methods", methodsValue)
			ctx.Response.Header.Set("Access-Control-Allow-Headers", headersValue)

			if allowCredentials {
				ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
			}
			ctx.Response.Header.Set("Access-Control-Max-Age", "86400")

			// 仅当是真正的 CORS 预检请求（带 Origin 与 Access-Control-Request-Method）
			// 时才短路返回 204；普通 OPTIONS 请求继续走后续路由处理器
			if string(ctx.Method()) == "OPTIONS" &&
				origin != "" &&
				len(ctx.Request.Header.Peek("Access-Control-Request-Method")) > 0 {
				ctx.SetStatusCode(fasthttp.StatusNoContent)
				return
			}

			next(ctx)
		}
	}
}

// resetErrorResponse 重建 500 错误响应：清理 handler 可能已写入的实体相关头
// （Content-Encoding/Content-Type/Content-Length 等），避免客户端按旧头解析错误
// 正文导致解码失败。保留请求追踪（X-Request-ID）与跨域头，便于排查。
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

// Recovery 恢复中间件，处理panic
func Recovery() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Panic recovered: %v\n%s", r, debug.Stack())
					resetErrorResponse(ctx)
				}
			}()

			next(ctx)
		}
	}
}

// maxRateLimitRPS 限流速率上限，避免 interval 除零、burst 溢出
const maxRateLimitRPS = 1_000_000

// RateLimit 限流中间件（惰性令牌桶，无后台 goroutine）
func RateLimit(requestsPerSecond int) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	if requestsPerSecond <= 0 {
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return func(ctx *fasthttp.RequestCtx) { next(ctx) }
		}
	}
	// 超过上限时钳制到上限，保证 interval 恒为正整数纳秒，burst 不溢出
	if requestsPerSecond > maxRateLimitRPS {
		requestsPerSecond = maxRateLimitRPS
	}

	burst := int64(requestsPerSecond * 2)
	var tokens atomic.Int64
	tokens.Store(burst)
	var lastRefill atomic.Int64
	lastRefill.Store(time.Now().UnixNano())
	var refillMu sync.Mutex
	interval := int64(time.Second) / int64(requestsPerSecond)

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			now := time.Now().UnixNano()
			last := lastRefill.Load()
			if now-last >= interval {
				refillMu.Lock()
				if last = lastRefill.Load(); now-last >= interval {
					// 时间戳按全部经过时间推进到当前时刻，令牌数单独封顶到 burst，
					// 避免空档期补充被截断后又用截断量推进时间、反复兑换令牌
					lastRefill.Store(now)
					if v := tokens.Add((now - last) / interval); v > burst {
						tokens.Store(burst)
					}
				}
				refillMu.Unlock()
			}

			if tokens.Add(-1) >= 0 {
				next(ctx)
			} else {
				tokens.Add(1) // 回退
				ctx.SetStatusCode(fasthttp.StatusTooManyRequests)
				ctx.SetBodyString("Too Many Requests")
			}
		}
	}
}

// Auth 认证中间件
func Auth(authFunc func(token string) bool) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// 从请求头获取token
			token := string(ctx.Request.Header.Peek("Authorization"))
			if token == "" {
				ctx.SetStatusCode(fasthttp.StatusUnauthorized)
				ctx.SetBodyString("Unauthorized")
				return
			}

			// 移除Bearer前缀
			if strings.HasPrefix(token, "Bearer ") {
				token = token[7:]
			}

			// 验证token
			if !authFunc(token) {
				ctx.SetStatusCode(fasthttp.StatusUnauthorized)
				ctx.SetBodyString("Invalid token")
				return
			}

			next(ctx)
		}
	}
}

// RequestID 请求ID中间件
func RequestID() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// 生成请求ID
			requestID := generateRequestID()

			// 设置请求ID到响应头
			ctx.Response.Header.Set("X-Request-ID", requestID)

			next(ctx)
		}
	}
}

var requestIDCounter uint64

// bufPool 复用 generateRequestID 的中间 buffer，避免每请求分配
var bufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 64)
		return &buf
	},
}

// generateRequestID 生成请求ID
func generateRequestID() string {
	id := atomic.AddUint64(&requestIDCounter, 1)
	bufPtr := bufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	buf = append(buf, "req_"...)
	buf = strconv.AppendInt(buf, time.Now().UnixNano(), 10)
	buf = append(buf, '_')
	buf = strconv.AppendUint(buf, id, 10)
	s := string(buf)
	*bufPtr = buf[:0]
	bufPool.Put(bufPtr)
	return s
}

// compressBufPool 复用压缩中间件的输出 buffer，减少 GC 压力
var compressBufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// acceptsGzip 按 Accept-Encoding 的 q 值协商判断客户端是否接受 gzip。
// 处理 gzip;q=0 / notgzip 这类拒绝表达；缺少 Accept-Encoding 时不做协商压缩，
// 保证与缓存键（编码维度）以及客户端实际解码能力一致。
func acceptsGzip(acceptEncoding string) bool {
	if acceptEncoding == "" {
		return false
	}
	for _, part := range strings.Split(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments := strings.Split(part, ";")
		coding := strings.TrimSpace(segments[0])
		if !strings.EqualFold(coding, "gzip") {
			continue
		}
		q := 1.0
		for _, param := range segments[1:] {
			param = strings.TrimSpace(param)
			if v, ok := strings.CutPrefix(param, "q="); ok {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = parsed
				}
			}
		}
		return q > 0
	}
	return false
}

// addVary 将字段追加到 Vary 头，保留既有的协商维度（如 CORS 的 Origin）
func addVary(h *fasthttp.ResponseHeader, field string) {
	existing := joinHeaderValues(h.PeekAll("Vary"))
	if existing == "" {
		h.Set("Vary", field)
		return
	}
	for _, v := range strings.Split(existing, ",") {
		if strings.EqualFold(strings.TrimSpace(v), field) {
			return
		}
	}
	h.Set("Vary", existing+", "+field)
}

// Compress 压缩中间件
func Compress() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	var gzipPool sync.Pool
	gzipPool.New = func() interface{} {
		return gzip.NewWriter(nil)
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			acceptEncoding := joinHeaderValues(ctx.Request.Header.PeekAll("Accept-Encoding"))
			next(ctx)
			// Every negotiated variant needs Vary, including identity responses.
			addVary(&ctx.Response.Header, "Accept-Encoding")
			if !acceptsGzip(acceptEncoding) || len(cacheDirectives(ctx.Response.Header.PeekAll("Cache-Control"))["no-transform"]) > 0 {
				return
			}

			// 流式响应不压缩：Response.Body() 会把流读到 EOF（SSE 会阻塞、大流全量入内存），
			// 读错误文本还会被当作正文返回
			if ctx.Response.IsBodyStream() {
				return
			}

			body := ctx.Response.Body()
			statusCode := ctx.Response.StatusCode()
			if len(body) < 256 || statusCode == fasthttp.StatusPartialContent || statusCode == fasthttp.StatusNoContent || statusCode == fasthttp.StatusNotModified ||
				len(ctx.Response.Header.Peek("Content-Encoding")) > 0 {
				return
			}

			buf := compressBufPool.Get().(*bytes.Buffer)
			buf.Reset()
			w := gzipPool.Get().(*gzip.Writer)
			w.Reset(buf)
			defer func() {
				// Detach the potentially large output before retaining the writer.
				w.Reset(io.Discard)
				gzipPool.Put(w)
				if buf.Cap() <= 256<<10 {
					buf.Reset()
					compressBufPool.Put(buf)
				}
			}()
			if _, err := w.Write(body); err != nil {
				w.Close()
				return
			}
			if err := w.Close(); err != nil {
				return
			}
			if buf.Len() >= len(body) {
				return
			}

			ctx.Response.SetBody(buf.Bytes())
			ctx.Response.Header.Set("Content-Encoding", "gzip")
			if etag := string(ctx.Response.Header.Peek("ETag")); etag != "" && !strings.HasPrefix(etag, "W/") {
				ctx.Response.Header.Del("ETag")
			}
			// 合并而非覆盖 Vary，保留压缩前已声明的协商维度（如 Origin）
			addVary(&ctx.Response.Header, "Accept-Encoding")
			ctx.Response.Header.Del("Content-Length")
		}
	}
}

// Security 安全头中间件
func Security() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// 设置安全头
			ctx.Response.Header.Set("X-Content-Type-Options", "nosniff")
			ctx.Response.Header.Set("X-Frame-Options", "DENY")
			ctx.Response.Header.Set("X-XSS-Protection", "1; mode=block")
			ctx.Response.Header.Set("Content-Security-Policy", "default-src 'self'")
			ctx.Response.Header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")

			// 执行下一个处理器
			next(ctx)
		}
	}
}

// Timeout 超时中间件。
//
// 组合约束：fasthttp 的超时实现会在独立 goroutine 中继续执行内部 handler，
// 超时返回后该 goroutine 可能仍在写响应。因此 Timeout 必须放在中间件链的
// 最外层（第一个注册），包住所有会在请求结束后读取响应的中间件（Logger、Compress、Cache）
// 与业务 handler，避免与并发的子 goroutine 产生数据竞争或缓存部分响应。
// 同样，恢复 panic 的逻辑必须位于业务 goroutine 内，位于 Timeout 外层的
// Recovery 无法捕获子 goroutine 的 panic。
// 超时只中止响应，不终止业务调用：需要协作退出的下游操作应使用可取消的
// 业务 context（core.RequestContext.Context()）。本中间件也在业务 goroutine 内恢复 panic。
func Timeout(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		if duration <= 0 {
			return next
		}
		recovered := Recovery()(next)
		return fasthttp.TimeoutHandler(func(ctx *fasthttp.RequestCtx) {
			businessCtx, cancel := context.WithTimeout(requestcontext.From(ctx), duration)
			defer cancel()
			requestcontext.Set(ctx, businessCtx)
			recovered(ctx)
		}, duration, "Request timeout")
	}
}
