package middleware

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/valyala/fasthttp"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func requestContext(t *testing.T, uri string, headers map[string]string) *fasthttp.RequestCtx {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI(uri)
	ctx.Request.Header.SetMethod("GET")
	for name, value := range headers {
		ctx.Request.Header.Set(name, value)
	}
	t.Cleanup(ctx.Response.Reset)
	return ctx
}

func TestCacheDoesNotSharePrivateResponses(t *testing.T) {
	for _, scenario := range []string{"authorization", "cookie", "private", "no-store", "no-cache", "set-cookie", "vary-star"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			handler := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
				calls++
				switch scenario {
				case "private", "no-store", "no-cache":
					ctx.Response.Header.Set("Cache-Control", scenario)
				case "set-cookie":
					ctx.Response.Header.Set("Set-Cookie", "session=secret")
				case "vary-star":
					ctx.Response.Header.Set("Vary", "*")
				}
				ctx.SetBodyString(fmt.Sprintf("response-%d", calls))
			})
			for _, user := range []string{"alice", "bob"} {
				headers := map[string]string{}
				if scenario == "authorization" {
					headers["Authorization"] = user
				}
				if scenario == "cookie" {
					headers["Cookie"] = "session=" + user
				}
				ctx := requestContext(t, "/profile", headers)
				handler(ctx)
				if string(ctx.Response.Header.Peek("X-Cache")) == "HIT" {
					t.Fatalf("private request %s hit shared cache", user)
				}
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestCacheAuthenticationCannotBeBypassed(t *testing.T) {
	for _, outside := range []bool{true, false} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			leaf := func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("private") }
			auth := Auth(func(token string) bool { return token == "valid" })
			cache := Cache(time.Minute)
			handler := cache(auth(leaf))
			if outside {
				handler = auth(cache(leaf))
			}
			good := requestContext(t, "/private", map[string]string{"Authorization": "Bearer valid"})
			handler(good)
			bad := requestContext(t, "/private", nil)
			handler(bad)
			if bad.Response.StatusCode() != 401 {
				t.Fatalf("anonymous request received %d %s", bad.Response.StatusCode(), bad.Response.Body())
			}
		})
	}
}

func TestCacheVaryHostAndDuplicateHeaders(t *testing.T) {
	calls := 0
	handler := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.Response.Header.Set("Vary", "Accept-Language")
		ctx.Response.Header.Add("Link", "</a>; rel=preload")
		ctx.Response.Header.Add("Link", "</b>; rel=preload")
		ctx.SetBodyString(string(ctx.Host()) + "/" + string(ctx.Request.Header.Peek("Accept-Language")))
	})
	for _, item := range []struct{ host, language, cache string }{
		{"a.test", "en", "MISS"}, {"a.test", "zh", "MISS"}, {"a.test", "en", "HIT"}, {"b.test", "en", "MISS"}, {"a.test", "zh", "HIT"},
	} {
		ctx := requestContext(t, "/", map[string]string{"Host": item.host, "Accept-Language": item.language})
		handler(ctx)
		if got := string(ctx.Response.Body()); got != item.host+"/"+item.language {
			t.Fatalf("wrong representation %q", got)
		}
		if got := string(ctx.Response.Header.Peek("X-Cache")); got != item.cache {
			t.Fatalf("cache=%q want %q", got, item.cache)
		}
		if got := len(ctx.Response.Header.PeekAll("Link")); got != 2 {
			t.Fatalf("lost repeated Link header: %d", got)
		}
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestCacheRequestIDRemainsUniqueInBothOrders(t *testing.T) {
	for _, outside := range []bool{true, false} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			leaf := func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("public") }
			handler := Cache(time.Minute)(RequestID()(leaf))
			if outside {
				handler = RequestID()(Cache(time.Minute)(leaf))
			}
			first := requestContext(t, "/", nil)
			handler(first)
			second := requestContext(t, "/", nil)
			handler(second)
			firstID, secondID := string(first.Response.Header.Peek("X-Request-ID")), string(second.Response.Header.Peek("X-Request-ID"))
			if firstID == "" || secondID == "" || firstID == secondID {
				t.Fatalf("request IDs %q %q", firstID, secondID)
			}
			if string(second.Response.Header.Peek("X-Cache")) != "HIT" {
				t.Fatal("not exercising cache hit")
			}
		})
	}
}

func TestCacheHonorsRequestDirectivesAndBodyLimit(t *testing.T) {
	for _, scenario := range []string{"request-no-cache", "oversize", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			config := CacheConfig{Duration: time.Minute, MaxEntryBytes: 32}
			if scenario == "disabled" {
				config.Duration = 0
			}
			calls := 0
			handler := CacheWithConfig(config)(func(ctx *fasthttp.RequestCtx) {
				calls++
				body := "ok"
				if scenario == "oversize" {
					body = strings.Repeat("x", 33)
				}
				ctx.SetBodyString(body)
			})
			for i := 0; i < 2; i++ {
				headers := map[string]string{}
				if scenario == "request-no-cache" {
					headers["Cache-Control"] = "no-cache"
				}
				handler(requestContext(t, "/", headers))
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestCacheDoesNotRenewStaleDatesOrIgnoreRequestFreshness(t *testing.T) {
	t.Run("old-date", func(t *testing.T) {
		calls := 0
		handler := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
			calls++
			// Parsing models a proxied origin response. fasthttp deliberately
			// ignores Header.Set("Date", ...) for directly generated responses.
			raw := "HTTP/1.1 200 OK\r\nDate: " + time.Now().Add(-2*time.Minute).UTC().Format(http.TimeFormat) + "\r\nCache-Control: max-age=60\r\nContent-Length: 3\r\n\r\nold"
			if err := ctx.Response.Read(bufio.NewReader(strings.NewReader(raw))); err != nil {
				t.Fatal(err)
			}
		})
		handler(requestContext(t, "/", nil))
		handler(requestContext(t, "/", nil))
		if calls != 2 {
			t.Fatal("old response acquired a fresh cache lifetime")
		}
	})
	for _, directive := range []string{"max-age=1", "min-fresh=60"} {
		t.Run(directive, func(t *testing.T) {
			calls := 0
			handler := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
				calls++
				ctx.Response.Header.Set("Age", "30")
				ctx.SetBodyString("old")
			})
			handler(requestContext(t, "/", nil))
			request := requestContext(t, "/", map[string]string{"Cache-Control": directive})
			handler(request)
			if calls != 2 || string(request.Response.Header.Peek("X-Cache")) == "HIT" {
				t.Fatal("client freshness constraint ignored")
			}
		})
	}
}

type countingReader struct {
	reader *bytes.Reader
	reads  int
}

func (reader *countingReader) Read(buf []byte) (int, error) {
	reader.reads++
	return reader.reader.Read(buf)
}

func TestCacheAndCompressPreserveStreaming(t *testing.T) {
	for name, mw := range map[string]func(fasthttp.RequestHandler) fasthttp.RequestHandler{"cache": Cache(time.Minute), "compress": Compress()} {
		t.Run(name, func(t *testing.T) {
			reader := &countingReader{reader: bytes.NewReader(bytes.Repeat([]byte("x"), 4096))}
			handler := mw(func(ctx *fasthttp.RequestCtx) { ctx.SetContentType("text/event-stream"); ctx.SetBodyStream(reader, -1) })
			ctx := requestContext(t, "/events", map[string]string{"Accept-Encoding": "gzip"})
			handler(ctx)
			if reader.reads != 0 || !ctx.Response.IsBodyStream() {
				t.Fatalf("stream consumed: reads=%d stream=%v", reader.reads, ctx.Response.IsBodyStream())
			}
		})
	}
}

func TestCacheAndCompressSkipHijackedResponses(t *testing.T) {
	for name, middleware := range map[string]func(fasthttp.RequestHandler) fasthttp.RequestHandler{"cache": Cache(time.Minute), "compress": Compress()} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			handler := middleware(func(ctx *fasthttp.RequestCtx) {
				calls++
				ctx.SetBodyString(strings.Repeat("x", 512))
				ctx.Hijack(func(net.Conn) {})
			})
			for i := 0; i < 2; i++ {
				ctx := requestContext(t, "/", map[string]string{"Accept-Encoding": "gzip"})
				handler(ctx)
				if len(ctx.Response.Header.Peek("X-Cache")) != 0 || len(ctx.Response.Header.Peek("Content-Encoding")) != 0 {
					t.Fatal("hijacked response transformed")
				}
			}
			if calls != 2 {
				t.Fatal("hijacked response cached")
			}
		})
	}
}

func TestCacheConcurrentVariants(t *testing.T) {
	handler := CacheWithConfig(CacheConfig{Duration: 20 * time.Millisecond, MaxEntries: 12, MaxBytes: 8192})(func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.Set("Vary", "X-Language")
		ctx.SetBody(ctx.Request.Header.Peek("X-Language"))
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.SetRequestURI("/")
				ctx.Request.Header.Set("X-Language", fmt.Sprint(worker))
				handler(ctx)
				if got := string(ctx.Response.Body()); got != fmt.Sprint(worker) {
					t.Errorf("cross-variant response %q", got)
				}
				ctx.Response.Reset()
			}
		}(i)
	}
	wg.Wait()
}
