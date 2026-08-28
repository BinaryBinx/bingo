package middleware

import (
	"bytes"
	"log"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
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

			// 处理预检请求
			if string(ctx.Method()) == "OPTIONS" {
				ctx.SetStatusCode(fasthttp.StatusNoContent)
				return
			}

			next(ctx)
		}
	}
}

// Recovery 恢复中间件，处理panic
func Recovery() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Panic recovered: %v", r)
					ctx.SetStatusCode(fasthttp.StatusInternalServerError)
					ctx.SetBodyString("Internal Server Error")
				}
			}()

			next(ctx)
		}
	}
}

// RateLimit 限流中间件（简单实现）
func RateLimit(requestsPerSecond int) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	if requestsPerSecond <= 0 {
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return func(ctx *fasthttp.RequestCtx) {
				next(ctx)
			}
		}
	}

	burst := requestsPerSecond * 2
	tokens := make(chan struct{}, burst)
	for i := 0; i < burst; i++ {
		tokens <- struct{}{}
	}

	ticker := time.NewTicker(time.Second / time.Duration(requestsPerSecond))
	stopCh := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				select {
				case tokens <- struct{}{}:
				default:
				}
			case <-stopCh:
				ticker.Stop()
				return
			}
		}
	}()

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			select {
			case <-tokens:
				next(ctx)
			default:
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

// generateRequestID 生成请求ID
func generateRequestID() string {
	id := atomic.AddUint64(&requestIDCounter, 1)
	// 避免 fmt.Sprintf 的反射开销，使用 strconv 拼接
	buf := make([]byte, 0, len("req_")+40)
	buf = append(buf, "req_"...)
	buf = strconv.AppendInt(buf, time.Now().UnixNano(), 10)
	buf = append(buf, '_')
	buf = strconv.AppendUint(buf, id, 10)
	return string(buf)
}

// Cache 缓存中间件
func Cache(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	// 缓存容量上限，防止长期运行导致内存无限增长
	const maxCacheItems = 10000

	type cacheItem struct {
		body       []byte
		statusCode int
		headers    map[string]string
		time       time.Time
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
				if now.Sub(it.time) >= duration {
					delete(cache, k)
				}
			}
		}
		if len(cache) >= maxCacheItems {
			var oldestKey string
			var oldestTime time.Time
			first := true
			for k, it := range cache {
				if first || it.time.Before(oldestTime) {
					oldestKey, oldestTime, first = k, it.time, false
				}
			}
			delete(cache, oldestKey)
		}
		cache[key] = item
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if string(ctx.Method()) != "GET" {
				next(ctx)
				return
			}

			key := string(ctx.RequestURI())

			cacheMu.RLock()
			item, found := cache[key]
			cacheMu.RUnlock()

			if found && time.Since(item.time) < duration {
				ctx.Response.SetStatusCode(item.statusCode)
				ctx.Response.SetBody(item.body)
				for k, v := range item.headers {
					ctx.Response.Header.Set(k, v)
				}
				ctx.Response.Header.Set("X-Cache", "HIT")
				return
			}

			next(ctx)

			if ctx.Response.StatusCode() == fasthttp.StatusOK {
				headers := make(map[string]string)
				ctx.Response.Header.VisitAll(func(key, value []byte) {
					headers[string(key)] = string(value)
				})

				bodyCopy := make([]byte, len(ctx.Response.Body()))
				copy(bodyCopy, ctx.Response.Body())

				storeCacheItem(key, cacheItem{
					body:       bodyCopy,
					statusCode: ctx.Response.StatusCode(),
					headers:    headers,
					time:       time.Now(),
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

// Compress 压缩中间件
func Compress() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	var gzipPool sync.Pool
	gzipPool.New = func() interface{} {
		return gzip.NewWriter(nil)
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			acceptEncoding := string(ctx.Request.Header.Peek("Accept-Encoding"))
			if !strings.Contains(acceptEncoding, "gzip") {
				next(ctx)
				return
			}

			next(ctx)

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
			ctx.Response.Header.Set("Vary", "Accept-Encoding")
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

// Timeout 超时中间件
func Timeout(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		if duration <= 0 {
			return next
		}
		return fasthttp.TimeoutHandler(next, duration, "Request timeout")
	}
}

// Static 静态文件中间件
func Static(root string) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	const maxCacheableStaticFileSize = 1 << 20

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
	rootPrefix := rootAbs + string(os.PathSeparator)
	cache := make(map[string]staticCacheItem)
	cacheMu := sync.RWMutex{}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			requestPath, err := url.PathUnescape(string(ctx.Path()))
			if err != nil {
				ctx.Error("Bad Request", fasthttp.StatusBadRequest)
				return
			}
			requestPath = strings.ReplaceAll(requestPath, "\\", "/")
			cleanPath := path.Clean("/" + requestPath)
			if cleanPath == "/" {
				cleanPath = "/index.html"
			}

			relPath := strings.TrimPrefix(cleanPath, "/")
			filePath := filepath.Join(rootAbs, filepath.FromSlash(relPath))
			fileAbs, err := filepath.Abs(filePath)
			if err != nil {
				ctx.Error("Bad Request", fasthttp.StatusBadRequest)
				return
			}
			fileAbs = filepath.Clean(fileAbs)
			if fileAbs != rootAbs && !strings.HasPrefix(fileAbs, rootPrefix) {
				ctx.Error("Forbidden", fasthttp.StatusForbidden)
				return
			}

			// 检查文件是否存在
			if info, err := os.Stat(fileAbs); err == nil && !info.IsDir() {
				cacheable := info.Size() <= maxCacheableStaticFileSize
				if cacheable {
					cacheMu.RLock()
					item, found := cache[fileAbs]
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

				if !cacheable {
					// 大文件流式发送，避免整文件读入内存
					ctx.SendFile(fileAbs)
					return
				}

				// 小文件读入内存并缓存
				data, err := os.ReadFile(fileAbs)
				if err != nil {
					ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
					return
				}
				contentType := mime.TypeByExtension(filepath.Ext(fileAbs))
				if contentType != "" {
					ctx.SetContentType(contentType)
				}
				bodyCopy := make([]byte, len(data))
				copy(bodyCopy, data)
				cacheMu.Lock()
				cache[fileAbs] = staticCacheItem{
					body:        bodyCopy,
					contentType: contentType,
					modTime:     info.ModTime(),
					size:        info.Size(),
				}
				cacheMu.Unlock()
				ctx.SetStatusCode(fasthttp.StatusOK)
				ctx.SetBody(data)
				return
			}

			// 文件不存在，执行下一个处理器
			next(ctx)
		}
	}
}
