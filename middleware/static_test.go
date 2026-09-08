package middleware

import (
	"github.com/valyala/fasthttp"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStaticPercentAndUnsupportedMethod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "100%.txt"), []byte("percent"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := Static(root)(func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(405) })
	ctx := requestContext(t, "/100%25.txt", nil)
	handler(ctx)
	if ctx.Response.StatusCode() != 200 || string(ctx.Response.Body()) != "percent" {
		t.Fatalf("valid encoded filename rejected: %d", ctx.Response.StatusCode())
	}
	post := requestContext(t, "/100%25.txt", nil)
	post.Request.Header.SetMethod("POST")
	handler(post)
	if post.Response.StatusCode() != 405 {
		t.Fatal("POST unexpectedly served file")
	}
}

func TestStaticRejectsFilesystemEscape(t *testing.T) {
	parent := t.TempDir()
	root, outside := filepath.Join(parent, "public"), filepath.Join(parent, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if runtime.GOOS == "windows" {
		command := exec.Command("powershell", "-NoProfile", "-Command", "$ErrorActionPreference = 'Stop'; New-Item -ItemType Junction -Path $env:BINGO_STATIC_TEST_LINK -Target $env:BINGO_STATIC_TEST_OUT | Out-Null")
		command.Env = append(os.Environ(), "BINGO_STATIC_TEST_LINK="+link, "BINGO_STATIC_TEST_OUT="+outside)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	managed, err := NewStaticHandler(root, StaticConfig{Immutable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	next := func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(404) }
	for _, handler := range []fasthttp.RequestHandler{Static(root)(next), managed.Middleware(next)} {
		ctx := requestContext(t, "/link/secret.txt", nil)
		handler(ctx)
		if ctx.Response.StatusCode() == 200 || string(ctx.Response.Body()) == "secret" {
			t.Fatal("escaped static root")
		}
	}
}

func TestStaticFullCacheUpdatesAndStreamsLargeFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "small.txt")
	if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := StaticWithConfig(root, StaticConfig{MaxEntries: 1, MaxBytes: 4096, MaxEntryBytes: 32, TTL: time.Second})(func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(404) })
	first := requestContext(t, "/small.txt", nil)
	handler(first)
	if err := os.WriteFile(file, []byte("new version"), 0600); err != nil {
		t.Fatal(err)
	}
	second := requestContext(t, "/small.txt", nil)
	handler(second)
	third := requestContext(t, "/small.txt", nil)
	handler(third)
	if string(second.Response.Body()) != "new version" || string(third.Response.Body()) != "new version" {
		t.Fatal("stale full cache")
	}
	large := strings.Repeat("x", 128)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := requestContext(t, "/large.txt", nil)
	handler(ctx)
	if !ctx.Response.IsBodyStream() {
		t.Fatal("large file buffered")
	}
	if string(ctx.Response.Body()) != large {
		t.Fatal("large file content damaged")
	}
	head := requestContext(t, "/large.txt", nil)
	head.Request.Header.SetMethod("HEAD")
	handler(head)
	if len(head.Response.Body()) != 0 || string(head.Response.Header.Peek("Content-Length")) != "128" {
		t.Fatal("incorrect HEAD")
	}
}

func TestStaticDoesNotOpenFIFO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFO is a Unix filesystem feature")
	}
	root := t.TempDir()
	if output, err := exec.Command("mkfifo", filepath.Join(root, "pipe")).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v %s", err, output)
	}
	handler := Static(root)(func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(404) })
	ctx := requestContext(t, "/pipe", nil)
	handler(ctx)
	if ctx.Response.StatusCode() != 404 {
		t.Fatal("non-regular file accepted")
	}
}
