package middleware

import (
	"bytes"
	"io"
	"log"
	"mime"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
				ctx.Response.Header.Set("Vary", "Origin")
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

// Cache 缓存中间件。
//
// 仅缓存显式可共享的 GET 响应，避免跨用户串数据：
//   - 带 Authorization/Cookie 的请求不命中也不缓存，认证逻辑不会被缓存命中绕过；
//   - 带 Set-Cookie 或 Cache-Control: private/no-store/no-cache 的响应不缓存；
//   - 流式响应（SSE、大文件）不缓存，避免把无终点的流整体读入内存；
//   - 缓存键包含 Host、URI、Accept-Encoding 与 Origin，正确处理编码协商与跨域维度；
//   - 响应声明了除 Accept-Encoding/Origin 之外的其他 Vary 字段时同样不缓存；
//   - 响应头按序保存（支持同名字段重复出现，如 Set-Cookie 场景下的多值）。
func Cache(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	// 缓存容量上限，防止长期运行导致内存无限增长
	const maxCacheItems = 10000

	type cacheHeader struct {
		key   string
		value string
	}

	type cacheItem struct {
		body       []byte
		statusCode int
		headers    []cacheHeader
		stored     time.Time
	}

	cache := make(map[string]cacheItem)
	cacheMu := sync.RWMutex{}

	// storeCacheItem 写入缓存，超出容量上限时先淘汰过期项，仍超限则淘汰最旧项
	storeCacheItem := func(key string, item cacheItem) {
		cacheMu.Lock()
		defer cacheMu.Unlock()

		if len(cache) >= maxCacheItems {
			now := time.Now()
			for k, it := range cache {
				if now.Sub(it.stored) >= duration {
					delete(cache, k)
				}
			}
		}
		if len(cache) >= maxCacheItems {
			var oldestKey string
			var oldestTime time.Time
			first := true
			for k, it := range cache {
				if first || it.stored.Before(oldestTime) {
					oldestKey, oldestTime, first = k, it.stored, false
				}
			}
			delete(cache, oldestKey)
		}
		cache[key] = item
	}

	// cacheKey 生成缓存键：包含 Host、URI、Accept-Encoding 与 Origin，
	// 避免同一路径在不同站点、不同编码协商或不同跨域来源之间串数据
	cacheKey := func(ctx *fasthttp.RequestCtx) string {
		host := string(ctx.Host())
		uri := string(ctx.RequestURI())
		ae := strings.ToLower(strings.TrimSpace(string(ctx.Request.Header.Peek("Accept-Encoding"))))
		origin := string(ctx.Request.Header.Peek("Origin"))
		return host + "|" + uri + "|ae=" + ae + "|origin=" + origin
	}

	// sensitiveRequest 判断请求是否携带私有化信息，这类请求不参与共享缓存
	sensitiveRequest := func(ctx *fasthttp.RequestCtx) bool {
		return len(ctx.Request.Header.Peek("Authorization")) > 0 ||
			len(ctx.Request.Header.Peek("Cookie")) > 0 ||
			len(ctx.Request.Header.Peek("Cache-Control")) > 0
	}

	// cacheableResponse 判断响应能否安全放入共享缓存
	cacheableResponse := func(ctx *fasthttp.RequestCtx) bool {
		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			return false
		}
		if ctx.Response.IsBodyStream() {
			return false
		}
		h := &ctx.Response.Header
		if len(h.Peek("Set-Cookie")) > 0 {
			return false
		}
		if cc := strings.ToLower(string(h.Peek("Cache-Control"))); strings.Contains(cc, "private") ||
			strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
			return false
		}
		// 仅接受可被缓存键覆盖的 Vary 维度；Vary: * 或其它自定义字段一律不缓存
		if vary := h.Peek("Vary"); len(vary) > 0 {
			for _, v := range strings.Split(strings.ToLower(string(vary)), ",") {
				v = strings.TrimSpace(v)
				if v == "*" {
					return false
				}
				if v != "accept-encoding" && v != "origin" {
					return false
				}
			}
		}
		return true
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if string(ctx.Method()) != "GET" || sensitiveRequest(ctx) {
				next(ctx)
				return
			}

			key := cacheKey(ctx)

			cacheMu.RLock()
			item, found := cache[key]
			cacheMu.RUnlock()

			if found && time.Since(item.stored) < duration {
				ctx.Response.ResetBody()
				ctx.Response.SetStatusCode(item.statusCode)
				ctx.Response.SetBody(item.body)
				for _, hdr := range item.headers {
					ctx.Response.Header.Set(hdr.key, hdr.value)
				}
				ctx.Response.Header.Set("X-Cache", "HIT")
				return
			}

			next(ctx)

			if cacheableResponse(ctx) {
				headers := make([]cacheHeader, 0, 8)
				ctx.Response.Header.VisitAll(func(key, value []byte) {
					ks := string(key)
					// 缓存快照中排除每次请求均不同、应逐请求生成的头
					if ks == "X-Cache" || ks == "X-Request-ID" {
						return
					}
					headers = append(headers, cacheHeader{ks, string(value)})
				})

				body := ctx.Response.Body()
				bodyCopy := make([]byte, len(body))
				copy(bodyCopy, body)

				storeCacheItem(key, cacheItem{
					body:       bodyCopy,
					statusCode: ctx.Response.StatusCode(),
					headers:    headers,
					stored:     time.Now(),
				})

				ctx.Response.Header.Set("X-Cache", "MISS")
			}
		}
	}
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
	existing := string(h.Peek("Vary"))
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
			acceptEncoding := string(ctx.Request.Header.Peek("Accept-Encoding"))
			if !acceptsGzip(acceptEncoding) {
				next(ctx)
				return
			}

			next(ctx)

			// 流式响应不压缩：Response.Body() 会把流读到 EOF（SSE 会阻塞、大流全量入内存），
			// 读错误文本还会被当作正文返回
			if ctx.Response.IsBodyStream() {
				return
			}

			body := ctx.Response.Body()
			statusCode := ctx.Response.StatusCode()
			if len(body) < 256 || statusCode == fasthttp.StatusNoContent || statusCode == fasthttp.StatusNotModified ||
				len(ctx.Response.Header.Peek("Content-Encoding")) > 0 {
				return
			}

			buf := compressBufPool.Get().(*bytes.Buffer)
			buf.Reset()
			w := gzipPool.Get().(*gzip.Writer)
			w.Reset(buf)
			if _, err := w.Write(body); err != nil {
				w.Close()
				gzipPool.Put(w)
				compressBufPool.Put(buf)
				return
			}
			if err := w.Close(); err != nil {
				gzipPool.Put(w)
				compressBufPool.Put(buf)
				return
			}
			gzipPool.Put(w)

			ctx.Response.SetBody(buf.Bytes())
			compressBufPool.Put(buf)
			ctx.Response.Header.Set("Content-Encoding", "gzip")
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
// 最外层（最后注册），包住所有会在请求结束后读取响应的中间件（Compress、Cache）
// 与业务 handler，避免与并发的子 goroutine 产生数据竞争或缓存部分响应。
// 同样，恢复 panic 的逻辑必须位于业务 goroutine 内，位于 Timeout 外层的
// Recovery 无法捕获子 goroutine 的 panic。
// 超时只中止响应，不终止业务调用：需要协作退出的下游操作应使用可取消的
// 业务 context。
func Timeout(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		if duration <= 0 {
			return next
		}
		return fasthttp.TimeoutHandler(next, duration, "Request timeout")
	}
}

// Static 静态文件中间件。
//
// 以目录句柄（os.Root）约束文件访问边界：所有 Stat/Open 都基于句柄解析，
// 目录内指向根目录以外的符号链接 / NTFS junction 会被拒绝，避免字符串前缀
// 检查被目录链接绕过。ctx.Path() 已解码，这里不再二次 PathUnescape。
func Static(root string) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	const maxCacheableStaticFileSize = 1 << 20
	// 缓存容量上限，防止静态文件持续更新/新增导致内存无限增长
	const maxStaticCacheItems = 1000

	type staticCacheItem struct {
		body        []byte
		contentType string
		modTime     time.Time
		size        int64
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		rootAbs = filepath.Clean(root)
	}
	rootAbs = filepath.Clean(rootAbs)

	cache := make(map[string]staticCacheItem)
	cacheMu := sync.RWMutex{}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// 每请求动态打开根目录句柄：既约束了访问边界，又不长期占用
			// 目录句柄（Windows 下常驻句柄会阻止目录删除/移动）
			rootHandle, err := os.OpenRoot(rootAbs)
			if err != nil {
				next(ctx)
				return
			}
			defer rootHandle.Close()

			requestPath := string(ctx.Path())
			if len(requestPath) == 0 {
				requestPath = "/"
			}
			// 仅在包含反斜杠时才替换，避免无谓的字符串分配
			if strings.Contains(requestPath, "\\") {
				requestPath = strings.ReplaceAll(requestPath, "\\", "/")
			}
			cleanPath := path.Clean("/" + requestPath)
			if cleanPath == "/" {
				cleanPath = "/index.html"
			}
			relPath := strings.TrimPrefix(cleanPath, "/")
			relPath = filepath.FromSlash(relPath)
			if relPath == "" {
				relPath = "."
			}

			// rootHandle.Stat 基于句柄解析，越界符号链接会在这里被拒绝
			info, err := rootHandle.Stat(relPath)
			if err != nil || info.IsDir() {
				// 文件不存在或为目录，执行下一个处理器
				next(ctx)
				return
			}

			cacheable := info.Size() <= maxCacheableStaticFileSize
			if cacheable {
				cacheMu.RLock()
				item, found := cache[relPath]
				cacheMu.RUnlock()
				if found && item.modTime.Equal(info.ModTime()) && item.size == info.Size() {
					if item.contentType != "" {
						ctx.SetContentType(item.contentType)
					}
					ctx.SetStatusCode(fasthttp.StatusOK)
					ctx.SetBody(item.body)
					return
				}
			}

			f, err := rootHandle.Open(relPath)
			if err != nil {
				next(ctx)
				return
			}

			if !cacheable {
				// 大文件流式发送，避免整文件读入内存；fasthttp 读完数据后会自动关闭流
				finfo, ferr := f.Stat()
				if ferr != nil {
					f.Close()
					ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
					return
				}
				if finfo.IsDir() {
					f.Close()
					next(ctx)
					return
				}
				if contentType := mime.TypeByExtension(filepath.Ext(relPath)); contentType != "" {
					ctx.SetContentType(contentType)
				}
				ctx.SetStatusCode(fasthttp.StatusOK)
				ctx.SetBodyStream(f, int(finfo.Size()))
				return
			}

			// 同一句柄有界读取：stat 与读取之间文件变大时也能限制读入量
			data, rerr := io.ReadAll(io.LimitReader(f, maxCacheableStaticFileSize+1))
			f.Close()
			if rerr != nil {
				ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
				return
			}
			if int64(len(data)) > maxCacheableStaticFileSize {
				// 文件在 stat 与读取之间变大，超出缓存阈值，直接整体发送不缓存
				if contentType := mime.TypeByExtension(filepath.Ext(relPath)); contentType != "" {
					ctx.SetContentType(contentType)
				}
				ctx.SetStatusCode(fasthttp.StatusOK)
				ctx.SetBody(data)
				return
			}
			contentType := mime.TypeByExtension(filepath.Ext(relPath))
			if contentType != "" {
				ctx.SetContentType(contentType)
			}
			bodyCopy := make([]byte, len(data))
			copy(bodyCopy, data)
			cacheMu.Lock()
			// 已存在 key 的更新始终允许；新增 key 时才受容量上限约束
			if _, exists := cache[relPath]; exists {
				cache[relPath] = staticCacheItem{
					body:        bodyCopy,
					contentType: contentType,
					modTime:     info.ModTime(),
					size:        info.Size(),
				}
			} else if len(cache) < maxStaticCacheItems {
				cache[relPath] = staticCacheItem{
					body:        bodyCopy,
					contentType: contentType,
					modTime:     info.ModTime(),
					size:        info.Size(),
				}
			}
			cacheMu.Unlock()
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetBody(data)
		}
	}
}
