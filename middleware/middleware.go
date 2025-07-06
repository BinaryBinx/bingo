package middleware

import (
	"fmt"
	"log"
	"strings"
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
	// 这里使用简单的令牌桶算法
	tokens := make(chan struct{}, requestsPerSecond)
	ticker := time.NewTicker(time.Second / time.Duration(requestsPerSecond))

	// 定期添加令牌
	go func() {
		for range ticker.C {
			select {
			case tokens <- struct{}{}:
			default:
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
