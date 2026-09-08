package middleware

import (
	"fmt"
	"github.com/valyala/fasthttp"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReviewStaticFIFO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix FIFO")
	}
	root := t.TempDir()
	path := filepath.Join(root, "pipe")
	if err := exec.Command("mkfifo", path).Run(); err != nil {
		t.Fatal(err)
	}
	h := Static(root)(func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(404) })
	ctx := newCacheGetRequest("/pipe", "", "", "")
	done := make(chan struct{})
	go func() { h(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("Static blocks opening FIFO")
		writer, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		writer.Close()
		<-done
	}
}

func TestReviewCacheRepeatedControlHeaders(t *testing.T) {
	calls := 0
	h := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.Response.Header.Add("Cache-Control", "public")
		ctx.Response.Header.Add("Cache-Control", "private, no-store")
		ctx.SetBodyString(fmt.Sprint(calls))
	})
	a, b := newCacheGetRequest("/", "", "", ""), newCacheGetRequest("/", "", "", "")
	h(a)
	h(b)
	if calls != 2 {
		t.Fatalf("private second Cache-Control ignored, calls=%d cache=%q headers=%q", calls, b.Response.Header.Peek("X-Cache"), a.Response.Header.PeekAll("Cache-Control"))
	}
}

func TestReviewCacheRepeatedVary(t *testing.T) {
	h := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.Add("Vary", "Origin")
		ctx.Response.Header.Add("Vary", "X-Tenant")
		ctx.SetBody(ctx.Request.Header.Peek("X-Tenant"))
	})
	a, b := newCacheGetRequest("/", "", "", ""), newCacheGetRequest("/", "", "", "")
	a.Request.Header.Set("X-Tenant", "alice")
	b.Request.Header.Set("X-Tenant", "bob")
	h(a)
	h(b)
	if string(b.Response.Body()) != "bob" {
		t.Fatalf("second Vary ignored: tenant bob received %q with %q", b.Response.Body(), b.Response.Header.Peek("X-Cache"))
	}
}

func TestReviewCacheStaleResponse(t *testing.T) {
	calls := 0
	h := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.Response.Header.Set("Cache-Control", "max-age=1")
		ctx.Response.Header.Set("Age", "100")
		ctx.SetBodyString("stale")
	})
	h(newCacheGetRequest("/", "", "", ""))
	b := newCacheGetRequest("/", "", "", "")
	h(b)
	if calls != 2 {
		t.Fatalf("stale response cached: %q age=%q", b.Response.Header.Peek("X-Cache"), b.Response.Header.Peek("Age"))
	}
}

func TestReviewCacheRepeatedOutput(t *testing.T) {
	h := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.Add("Link", "</a>")
		ctx.Response.Header.Add("Link", "</b>")
		ctx.SetBodyString("ok")
	})
	h(newCacheGetRequest("/", "", "", ""))
	b := newCacheGetRequest("/", "", "", "")
	h(b)
	if n := len(b.Response.Header.PeekAll("Link")); n != 2 {
		t.Fatalf("cache hit has %d Link headers", n)
	}
}

func TestReviewCompressionHTTPMetadata(t *testing.T) {
	for _, scenario := range []string{"identity-vary", "range", "no-transform", "strong-etag"} {
		t.Run(scenario, func(t *testing.T) {
			h := Compress()(func(ctx *fasthttp.RequestCtx) {
				ctx.SetBodyString(strings.Repeat("a", 512))
				switch scenario {
				case "range":
					ctx.SetStatusCode(206)
					ctx.Response.Header.Set("Content-Range", "bytes 0-511/1024")
				case "no-transform":
					ctx.Response.Header.Set("Cache-Control", "no-transform")
				case "strong-etag":
					ctx.Response.Header.Set("ETag", `"identity"`)
				}
			})
			ae := "gzip"
			if scenario == "identity-vary" {
				ae = "identity"
			}
			ctx := compressCtx(ae)
			h(ctx)
			if scenario == "identity-vary" && !strings.Contains(strings.ToLower(string(ctx.Response.Header.Peek("Vary"))), "accept-encoding") {
				t.Error("identity response missing Vary")
			}
			if (scenario == "range" || scenario == "no-transform") && len(ctx.Response.Header.Peek("Content-Encoding")) > 0 {
				t.Errorf("transformed %s response: headers=%s", scenario, ctx.Response.Header.Header())
			}
			if scenario == "strong-etag" && string(ctx.Response.Header.Peek("ETag")) == `"identity"` {
				t.Error("gzip reused identity strong ETag")
			}
		})
	}
}

func BenchmarkReviewLatestCacheChurn(b *testing.B) {
	h := Cache(time.Hour)(func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("public") })
	var ctx fasthttp.RequestCtx
	for i := 0; i < 10000; i++ {
		ctx.Request.SetRequestURI("/" + strconv.Itoa(i))
		h(&ctx)
		ctx.Response.Reset()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.Request.SetRequestURI("/" + strconv.Itoa(i+10000))
		h(&ctx)
		ctx.Response.Reset()
	}
}

func BenchmarkReviewLatestCacheHit(b *testing.B) {
	h := Cache(time.Hour)(func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("public") })
	var ctx fasthttp.RequestCtx
	ctx.Request.SetRequestURI("/")
	h(&ctx)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.Response.Reset()
		h(&ctx)
	}
}
