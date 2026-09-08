// Package middleware provides composable fasthttp middleware.
package middleware

import (
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
)

// LoggerConfig permits sampling or a custom logging sink. Printf is synchronous;
// an asynchronous sink must own its queue and shutdown lifecycle.
type LoggerConfig struct {
	SampleEvery uint64 // <= 1 logs every request.
	Printf      func(format string, args ...any)
}

func Logger() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return LoggerWithConfig(LoggerConfig{})
}

func LoggerWithConfig(config LoggerConfig) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	printf := config.Printf
	if printf == nil {
		printf = log.Printf
	}
	var sequence atomic.Uint64
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if config.SampleEvery > 1 && (sequence.Add(1)-1)%config.SampleEvery != 0 {
				next(ctx)
				return
			}
			start := time.Now()
			guard := guardResponse(ctx)
			// Timeout can return while its worker still owns ctx. Copy metadata
			// before next and only read the separate timeout snapshot afterwards.
			method, uri, remote := string(ctx.Method()), string(ctx.RequestURI()), ctx.RemoteAddr().String()
			next(ctx)
			var status int
			if guard.worker != nil {
				if guard.worker.Err() != nil {
					status = fasthttp.StatusRequestTimeout
				} else {
					status = ctx.Response.StatusCode()
				}
			} else if response := ctx.LastTimeoutErrorResponse(); response != nil {
				status = response.StatusCode()
			} else {
				status = ctx.Response.StatusCode()
			}
			printf("[%s] %s %s - %d - %v", method, uri, remote, status, time.Since(start))
		}
	}
}

func CORS(allowedOrigins, allowedMethods, allowedHeaders []string) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	// Own configuration data so callers can safely reuse their slices.
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origins[origin] = struct{}{}
	}
	_, allowAll := origins["*"]
	allowAll = allowAll || len(origins) == 0
	methods, headers := "GET, POST, PUT, DELETE, OPTIONS", "Content-Type, Authorization"
	if len(allowedMethods) > 0 {
		methods = strings.Join(allowedMethods, ", ")
	}
	if len(allowedHeaders) > 0 {
		headers = strings.Join(allowedHeaders, ", ")
	}
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			origin := string(ctx.Request.Header.Peek("Origin"))
			if !allowAll {
				addVary(&ctx.Response.Header, "Origin")
			}
			_, allowed := origins[origin]
			if allowAll {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
			} else if origin != "" && allowed {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
			}
			ctx.Response.Header.Set("Access-Control-Allow-Methods", methods)
			ctx.Response.Header.Set("Access-Control-Allow-Headers", headers)
			ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
			if ctx.IsOptions() && origin != "" && len(ctx.Request.Header.Peek("Access-Control-Request-Method")) != 0 {
				ctx.SetStatusCode(fasthttp.StatusNoContent)
				return
			}
			next(ctx)
		}
	}
}

func Recovery() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			insideTimeout := requestcontext.HasTimeout(ctx)
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Printf("Panic recovered: %v\n%s", recovered, debug.Stack())
					if insideTimeout || ctx.LastTimeoutErrorResponse() == nil {
						resetErrorResponse(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
					}
				}
			}()
			next(ctx)
		}
	}
}

// Remove stale entity headers after a partial response, preserving security and
// tracing headers. In particular plain errors must not retain gzip or Location.
func resetErrorResponse(ctx *fasthttp.RequestCtx, status int, body string) {
	for _, name := range []string{"Content-Encoding", "Content-Length", "Content-Range", "Transfer-Encoding", "ETag", "Last-Modified", "Location", "Set-Cookie"} {
		ctx.Response.Header.Del(name)
	}
	ctx.SetContentType("text/plain; charset=utf-8")
	ctx.Response.Header.Set("Cache-Control", "no-store")
	ctx.SetStatusCode(status)
	ctx.SetBodyString(body)
}

type tokenBucket struct {
	mu                     sync.Mutex
	rate, capacity, tokens float64
	last                   time.Time
}

func (bucket *tokenBucket) allow(now time.Time) bool {
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	// Sub uses the monotonic clock. Advance time independently of the token
	// cap, discarding credit accumulated while an idle bucket was full.
	if elapsed := now.Sub(bucket.last); elapsed > 0 {
		bucket.tokens = min(bucket.capacity, bucket.tokens+elapsed.Seconds()*bucket.rate)
		bucket.last = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// RateLimit is an application-wide token bucket with a two-second burst.
// Non-positive rates disable it. Float rates avoid integer overflow and zero
// nanosecond intervals. Refill and consumption share a single short critical section.
func RateLimit(requestsPerSecond int) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	if requestsPerSecond <= 0 {
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	}
	rate := float64(requestsPerSecond)
	bucket := &tokenBucket{rate: rate, capacity: rate * 2, tokens: rate * 2, last: time.Now()}
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if bucket.allow(time.Now()) {
				next(ctx)
				return
			}
			ctx.Response.Header.Set("Retry-After", "1")
			resetErrorResponse(ctx, fasthttp.StatusTooManyRequests, "Too Many Requests")
		}
	}
}

func Auth(authFunc func(token string) bool) func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			token := string(ctx.Request.Header.Peek("Authorization"))
			if len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ") {
				token = strings.TrimSpace(token[7:])
			}
			if token == "" || authFunc == nil || !authFunc(token) {
				ctx.Response.Header.Set("WWW-Authenticate", "Bearer")
				resetErrorResponse(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
				return
			}
			next(ctx)
		}
	}
}

func RequestID() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.Response.Header.Set("X-Request-ID", generateRequestID())
			next(ctx)
		}
	}
}

var requestIDCounter atomic.Uint64

func generateRequestID() string {
	// Only the returned immutable string is allocated; the scratch array is local.
	var storage [64]byte
	buf := append(storage[:0], "req_"...)
	buf = strconv.AppendInt(buf, time.Now().UnixNano(), 10)
	buf = append(buf, '_')
	buf = strconv.AppendUint(buf, requestIDCounter.Add(1), 10)
	return string(buf)
}

func Security() func(fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.Response.Header.Set("X-Content-Type-Options", "nosniff")
			ctx.Response.Header.Set("X-Frame-Options", "DENY")
			ctx.Response.Header.Set("X-XSS-Protection", "1; mode=block")
			ctx.Response.Header.Set("Content-Security-Policy", "default-src 'self'")
			ctx.Response.Header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			next(ctx)
		}
	}
}
