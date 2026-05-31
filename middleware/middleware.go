package middleware

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// 检查是否允许该源
			if len(allowedOrigins) > 0 {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", strings.Join(allowedOrigins, ", "))
			} else {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
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

			ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
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
	tokens := make(chan struct{}, requestsPerSecond*2)
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

// generateRequestID 生成请求ID
func generateRequestID() string {
	// 这里可以使用UUID或其他方式生成唯一ID
	// 简单实现使用时间戳
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
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
			if len(body) < 256 {
				return
			}

			var buf bytes.Buffer
			w := gzipPool.Get().(*gzip.Writer)
			w.Reset(&buf)
			w.Write(body)
			w.Close()
			gzipPool.Put(w)

			ctx.Response.SetBody(buf.Bytes())
			ctx.Response.Header.Set("Content-Encoding", "gzip")
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
		return func(ctx *fasthttp.RequestCtx) {
			done := make(chan struct{}, 1)

			go func() {
				next(ctx)
				close(done)
			}()

			select {
			case <-done:
			case <-time.After(duration):
				ctx.Error("Request timeout", fasthttp.StatusRequestTimeout)
			}
		}
	}
}

// Static 静态文件中间件
func Static(root string) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// 构建文件路径
			path := string(ctx.RequestURI())
			if path == "/" {
				path = "/index.html"
			}
			filePath := filepath.Join(root, path)

			// 检查文件是否存在
			if _, err := os.Stat(filePath); err == nil {
				// 服务静态文件
				fasthttp.ServeFile(ctx, filePath)
				return
			}

			// 文件不存在，执行下一个处理器
			next(ctx)
		}
	}
}
