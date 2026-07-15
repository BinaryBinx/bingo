package middleware

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"log"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	originAllowed := func(origin string) bool {
		for _, allowedOrigin := range allowedOrigins {
			if allowedOrigin == "*" || allowedOrigin == origin {
				return true
			}
		}
		return false
	}

	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowCredentials := false

			switch {
			case len(allowedOrigins) == 0:
				ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
			case origin != "" && originAllowed(origin):
				for _, allowedOrigin := range allowedOrigins {
					if allowedOrigin == "*" {
						ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
						break
					}
				}
				if string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) == "" {
					ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
					ctx.Response.Header.Set("Vary", "Origin")
					allowCredentials = true
				}
			}

			// 设置其他CORS头
			if len(allowedMethods) > 0 {
				ctx.Response.Header.Set("Access-Control-Allow-Methods", strings.Join(allowedMethods, ", "))
			} else {
				ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			}

			if len(allowedHeaders) > 0 {
				ctx.Response.Header.Set("Access-Control-Allow-Headers", strings.Join(allowedHeaders, ", "))
			} else {
				ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			}

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
	return fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), id)
}

// Cache 缓存中间件
func Cache(duration time.Duration) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	type cacheItem struct {
		body       []byte
		statusCode int
		headers    map[string]string
		time       time.Time
	}

	cache := make(map[string]cacheItem)
	cacheMu := sync.RWMutex{}

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

				cacheMu.Lock()
				cache[key] = cacheItem{
					body:       bodyCopy,
					statusCode: ctx.Response.StatusCode(),
					headers:    headers,
					time:       time.Now(),
				}
				cacheMu.Unlock()

				ctx.Response.Header.Set("X-Cache", "MISS")
			}
		}
	}
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

			var buf bytes.Buffer
			w := gzipPool.Get().(*gzip.Writer)
			w.Reset(&buf)
			if _, err := w.Write(body); err != nil {
				w.Close()
				gzipPool.Put(w)
				return
			}
			if err := w.Close(); err != nil {
				gzipPool.Put(w)
				return
			}
			gzipPool.Put(w)

			ctx.Response.SetBody(buf.Bytes())
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

				// 服务静态文件
				data, err := os.ReadFile(fileAbs)
				if err != nil {
					ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
					return
				}
				contentType := mime.TypeByExtension(filepath.Ext(fileAbs))
				if contentType != "" {
					ctx.SetContentType(contentType)
				}
				if cacheable {
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
				}
				ctx.SetStatusCode(fasthttp.StatusOK)
				ctx.SetBody(data)
				return
			}

			// 文件不存在，执行下一个处理器
			next(ctx)
		}
	}
}
