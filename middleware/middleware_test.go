package middleware

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
