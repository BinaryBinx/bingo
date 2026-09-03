package middleware

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCORSReflectsAllowedOrigin(t *testing.T) {
	handler := CORS([]string{"https://example.com"}, nil, nil)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/")
	ctx.Request.Header.Set("Origin", "https://example.com")

	handler(ctx)

	if got := string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")); got != "https://example.com" {
		t.Fatalf("expected reflected origin, got %q", got)
	}
	if got := string(ctx.Response.Header.Peek("Access-Control-Allow-Credentials")); got != "true" {
		t.Fatalf("expected credentials for reflected origin, got %q", got)
	}
}

func TestCORSDoesNotAllowUnknownOrigin(t *testing.T) {
	handler := CORS([]string{"https://example.com"}, nil, nil)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/")
	ctx.Request.Header.Set("Origin", "https://evil.example")

	handler(ctx)

	if got := string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")); got != "" {
		t.Fatalf("expected no allow-origin header for unknown origin, got %q", got)
	}
}

func TestRateLimitNonPositiveValueIsNoop(t *testing.T) {
	called := false
	handler := RateLimit(0)(func(ctx *fasthttp.RequestCtx) {
		called = true
		ctx.SetStatusCode(http.StatusOK)
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/")
	defer ctx.Response.Reset()

	handler(ctx)

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if ctx.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, ctx.Response.StatusCode())
	}
}

func TestStaticServesFilesInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := Static(root)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusNotFound)
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/")

	handler(ctx)

	if ctx.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Body()); got != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", got)
	}
}

func TestStaticBlocksPathTraversal(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "public")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := Static(root)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusNotFound)
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/../secret.txt")
	defer ctx.Response.Reset()

	handler(ctx)

	if ctx.Response.StatusCode() == http.StatusOK {
		t.Fatal("expected traversal request not to be served")
	}
	if got := string(ctx.Response.Body()); got == "secret" {
		t.Fatal("static middleware leaked a file outside the root")
	}
}

// ---- P1: Cache 缓存隔离回归测试 ----

func newCacheGetRequest(uri, cookie, auth, acceptEncoding string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI(uri)
	if cookie != "" {
		ctx.Request.Header.Set("Cookie", cookie)
	}
	if auth != "" {
		ctx.Request.Header.Set("Authorization", "Bearer "+auth)
	}
	if acceptEncoding != "" {
		ctx.Request.Header.Set("Accept-Encoding", acceptEncoding)
	}
	return ctx
}

// TestCacheIsolatesPrivateRequests 验证带 Cookie/Authorization 的请求不命中缓存、响应也不缓存，
// 避免跨用户串私有正文
func TestCacheIsolatesPrivateRequests(t *testing.T) {
	var calls int
	handler := Cache(10 * time.Second)(func(ctx *fasthttp.RequestCtx) {
		calls++
		sess := string(ctx.Request.Header.Peek("Cookie"))
		ctx.SetStatusCode(http.StatusOK)
		ctx.SetBodyString("data-for:" + sess)
	})

	reqAlice := newCacheGetRequest("/private", "alice", "", "")
	handler(reqAlice)
	if got := string(reqAlice.Response.Body()); got != "data-for:alice" {
		t.Fatalf("expected alice data, got %q", got)
	}

	// Bob 带不同 Cookie：必须重新执行 handler，不能命中 Alice 的响应
	reqBob := newCacheGetRequest("/private", "bob", "", "")
	handler(reqBob)
	if calls != 2 {
		t.Fatalf("expected handler to run for Bob, calls=%d", calls)
	}
	if got := string(reqBob.Response.Body()); got != "data-for:bob" {
		t.Fatalf("expected bob data, got %q", got)
	}
}

// TestCacheDoesNotSkipAuth 验证认证不能被缓存命中绕过
func TestCacheDoesNotSkipAuth(t *testing.T) {
	var calls int
	handler := Cache(10 * time.Second)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.SetStatusCode(http.StatusOK)
		ctx.SetBodyString("ok")
	})

	// 带 Authorization 的请求每次都执行 handler（不命中、不缓存）
	for i := 0; i < 2; i++ {
		req := newCacheGetRequest("/api", "", "token1", "")
		handler(req)
	}
	if calls != 2 {
		t.Fatalf("auth requests must never hit cache, calls=%d", calls)
	}
}

// TestCacheSkipsSetCookieResponses 验证带 Set-Cookie 的响应不被缓存
func TestCacheSkipsSetCookieResponses(t *testing.T) {
	var calls int
	handler := Cache(10 * time.Second)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.SetStatusCode(http.StatusOK)
		ctx.Response.Header.Set("Set-Cookie", "session=abc")
		ctx.SetBodyString("hello")
	})

	req := newCacheGetRequest("/login", "", "", "")
	handler(req)
	req2 := newCacheGetRequest("/login", "", "", "")
	handler(req2)
	if calls != 2 {
		t.Fatalf("Set-Cookie responses must not be cached, calls=%d", calls)
	}
}

// TestCacheHitsReusableResponse 正常可共享响应命中缓存
func TestCacheHitsReusableResponse(t *testing.T) {
	var calls int
	handler := Cache(10 * time.Second)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.SetStatusCode(http.StatusOK)
		ctx.SetBodyString("public-data")
	})

	req := newCacheGetRequest("/public", "", "", "")
	handler(req)
	if got := string(req.Response.Header.Peek("X-Cache")); got != "MISS" {
		t.Fatalf("expected first request MISS, got %q", got)
	}

	req2 := newCacheGetRequest("/public", "", "", "")
	handler(req2)
	if got := string(req2.Response.Header.Peek("X-Cache")); got != "HIT" {
		t.Fatalf("expected second request HIT, got %q", got)
	}
	if calls != 1 {
		t.Fatalf("expected handler to run once, calls=%d", calls)
	}
	if got := string(req2.Response.Body()); got != "public-data" {
		t.Fatalf("expected cached body, got %q", got)
	}
}

// TestCacheEncodingNegotiation 验证 Cache(Compress()) 组合下编码协商维度隔离：
// gzip 与 identity 各自独立缓存
func TestCacheEncodingNegotiation(t *testing.T) {
	var calls int
	handler := Cache(10 * time.Second)(Compress()(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.SetStatusCode(http.StatusOK)
		ctx.SetBodyString(strings.Repeat("a", 1000))
	}))

	// 首次 gzip 预热
	req1 := newCacheGetRequest("/data", "", "", "gzip")
	handler(req1)
	if got := string(req1.Response.Header.Peek("Content-Encoding")); got != "gzip" {
		t.Fatalf("expected gzip encoding, got %q", got)
	}

	// 显式 identity：应重新生成未压缩响应，而不是把 gzip 内容伪装成 identity
	req2 := newCacheGetRequest("/data", "", "", "identity")
	handler(req2)
	if got := string(req2.Response.Header.Peek("Content-Encoding")); got != "" {
		t.Fatalf("expected no Content-Encoding for identity, got %q", got)
	}
	if calls != 2 {
		t.Fatalf("expected regenerated response for identity, calls=%d", calls)
	}
}

// ---- P2: RateLimit 回归测试 ----

func ratelimitCtx() *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/")
	return ctx
}

// TestRateLimitEnforcesBurst 连续请求不能超过 burst 容量
func TestRateLimitEnforcesBurst(t *testing.T) {
	handler := RateLimit(1)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
	})

	got := make([]int, 4)
	for i := 0; i < 4; i++ {
		ctx := ratelimitCtx()
		handler(ctx)
		got[i] = ctx.Response.StatusCode()
	}
	// burst=2：前两个通过，之后限流
	if got[0] != http.StatusOK || got[1] != http.StatusOK || got[2] != http.StatusTooManyRequests || got[3] != http.StatusTooManyRequests {
		t.Fatalf("unexpected statuses: %v", got)
	}
}

// TestRateLimitNoExtraBurstAfterIdle 空闲期不能反复兑换超过 burst 的令牌
func TestRateLimitNoExtraBurstAfterIdle(t *testing.T) {
	handler := RateLimit(1)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
	})

	// 打空桶
	for i := 0; i < 2; i++ {
		ctx := ratelimitCtx()
		handler(ctx)
	}

	// 闲置超过 interval 后补充，令牌数必须被 burst 封顶
	time.Sleep(4100 * time.Millisecond)

	passed := 0
	for i := 0; i < 4; i++ {
		ctx := ratelimitCtx()
		handler(ctx)
		if ctx.Response.StatusCode() == http.StatusOK {
			passed++
		}
	}
	if passed > 2 {
		t.Fatalf("idle time must not create extra burst tokens beyond capacity, passed=%d", passed)
	}
}

// TestRateLimitHugeRPSNoPanic 过大的速率不得触发除零 panic
func TestRateLimitHugeRPSNoPanic(t *testing.T) {
	handler := RateLimit(1_000_000_000)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
	})
	ctx := ratelimitCtx()
	handler(ctx)
	if ctx.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected OK, got %d", ctx.Response.StatusCode())
	}
}

// ---- P2: Compress 协商回归测试 ----

func compressCtx(acceptEncoding string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/")
	if acceptEncoding != "" {
		ctx.Request.Header.Set("Accept-Encoding", acceptEncoding)
	}
	return ctx
}

// TestCompressRespectsGzipQZero Accept-Encoding: gzip;q=0 时不压缩
func TestCompressRespectsGzipQZero(t *testing.T) {
	handler := Compress()(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
		ctx.SetBodyString(strings.Repeat("b", 1000))
	})

	for _, ae := range []string{"gzip;q=0", "identity", "br", "notgzip"} {
		ctx := compressCtx(ae)
		handler(ctx)
		if got := string(ctx.Response.Header.Peek("Content-Encoding")); got != "" {
			t.Fatalf("ae=%q must not compress, got Content-Encoding=%q", ae, got)
		}
	}
}

// TestCompressPreservesExistingVary 压缩时应合并而非覆盖已有的 Vary 头
func TestCompressPreservesExistingVary(t *testing.T) {
	handler := Compress()(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
		ctx.Response.Header.Set("Vary", "Origin")
		ctx.SetBodyString(strings.Repeat("c", 1000))
	})

	ctx := compressCtx("gzip")
	handler(ctx)
	vary := string(ctx.Response.Header.Peek("Vary"))
	if !strings.Contains(strings.ToLower(vary), "origin") || !strings.Contains(strings.ToLower(vary), "accept-encoding") {
		t.Fatalf("expected Vary to merge Accept-Encoding into Origin, got %q", vary)
	}
}

// TestCompressSkipsStreamingResponse 流式响应不压缩
func TestCompressSkipsStreamingResponse(t *testing.T) {
	handler := Compress()(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusOK)
		ctx.Response.SetBodyStream(bytes.NewReader([]byte(strings.Repeat("d", 1000))), 1000)
	})

	ctx := compressCtx("gzip")
	handler(ctx)
	if got := string(ctx.Response.Header.Peek("Content-Encoding")); got != "" {
		t.Fatalf("streaming responses must not be compressed, got %q", got)
	}
}

// ---- P2: Recovery / CORS / Static 回归测试 ----

// TestRecoveryClearsEntityHeaders handler 写入实体头后 panic，错误响应不得残留旧头
func TestRecoveryClearsEntityHeaders(t *testing.T) {
	handler := Recovery()(func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.Set("Content-Encoding", "gzip")
		ctx.Response.Header.Set("Content-Type", "application/json")
		ctx.Response.Header.Set("X-Request-ID", "req-1")
		ctx.SetBodyString("{\"x\":1}")
		panic("boom")
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/")
	handler(ctx)

	if ctx.Response.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Header.Peek("Content-Encoding")); got != "" {
		t.Fatalf("expected Content-Encoding cleared, got %q", got)
	}
	if got := string(ctx.Response.Header.Peek("Content-Type")); !strings.Contains(got, "text/plain") {
		t.Fatalf("expected text/plain Content-Type, got %q", got)
	}
	if got := string(ctx.Response.Header.Peek("X-Request-ID")); got != "req-1" {
		t.Fatalf("expected X-Request-ID preserved, got %q", got)
	}
}

// TestCORSPreflightOnlyForActualPreflight 普通 OPTIONS 请求不被 CORS 短路
func TestCORSPreflightOnlyForActualPreflight(t *testing.T) {
	var nextCalled bool
	handler := CORS(nil, nil, nil)(func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
		ctx.SetStatusCode(http.StatusOK)
	})

	// 普通 OPTIONS：没有 Origin 与 Access-Control-Request-Method，应继续
	plainOpts := &fasthttp.RequestCtx{}
	plainOpts.Request.Header.SetMethod("OPTIONS")
	plainOpts.Request.SetRequestURI("/resource")
	handler(plainOpts)
	if !nextCalled {
		t.Fatal("plain OPTIONS request must reach the next handler")
	}

	// 真实预检：带 Origin 与 Access-Control-Request-Method，应短路 204
	nextCalled = false
	preflight := &fasthttp.RequestCtx{}
	preflight.Request.Header.SetMethod("OPTIONS")
	preflight.Request.SetRequestURI("/resource")
	preflight.Request.Header.Set("Origin", "https://example.com")
	preflight.Request.Header.Set("Access-Control-Request-Method", "POST")
	handler(preflight)
	if nextCalled {
		t.Fatal("preflight request must be short-circuited")
	}
	if preflight.Response.StatusCode() != fasthttp.StatusNoContent {
		t.Fatalf("expected 204 for preflight, got %d", preflight.Response.StatusCode())
	}
}

// TestStaticDecodesPathOnce 真实文件名包含百分号时，不再二次解码
func TestStaticDecodesPathOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "100%.txt"), []byte("percent"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := Static(root)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusNotFound)
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	// fasthttp 会先把 %25 解码为 %，中间件不应再 PathUnescape 一次
	ctx.Request.SetRequestURI("/100%25.txt")

	handler(ctx)

	if ctx.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%q)", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if got := string(ctx.Response.Body()); got != "percent" {
		t.Fatalf("expected file content, got %q", got)
	}
}

// TestStaticCacheRefreshesUpdatedFile 缓存已存在的 key 修改后应能写回
func TestStaticCacheRefreshesUpdatedFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "x.txt")
	if err := os.WriteFile(file, []byte("version-one"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := Static(root)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(http.StatusNotFound)
	})

	get := func() string {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/x.txt")
		handler(ctx)
		return string(ctx.Response.Body())
	}

	if got := get(); got != "version-one" {
		t.Fatalf("expected v1, got %q", got)
	}
	// 修改文件（内容长度不同，确保 size/modTime 校验能发现变更）后应反映新内容
	if err := os.WriteFile(file, []byte("version-two-updated"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "version-two-updated" {
		t.Fatalf("expected updated content, got %q", got)
	}
}
