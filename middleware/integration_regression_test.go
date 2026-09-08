package middleware

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestLoggerSamplingAndCustomSink(t *testing.T) {
	var messages []string
	calls := 0
	h := LoggerWithConfig(LoggerConfig{SampleEvery: 3, Printf: func(format string, args ...any) {
		messages = append(messages, fmt.Sprintf(format, args...))
	}})(func(ctx *fasthttp.RequestCtx) { calls++; ctx.SetStatusCode(201) })
	for i := 0; i < 7; i++ {
		h(requestContext(t, fmt.Sprintf("/request/%d", i), nil))
	}
	if calls != 7 || len(messages) != 3 {
		t.Fatalf("handler=%d logs=%d", calls, len(messages))
	}
	for i, message := range messages {
		if !strings.Contains(message, fmt.Sprintf("/request/%d", i*3)) || !strings.Contains(message, "201") {
			t.Fatalf("wrong sampled metadata: %q", message)
		}
	}
}

func TestAuthRejectsNilCallbackAndEmptyBearer(t *testing.T) {
	for _, token := range []string{"", "Bearer ", "bearer    ", "valid"} {
		ctx := requestContext(t, "/", map[string]string{"Authorization": token})
		Auth(nil)(func(*fasthttp.RequestCtx) { t.Fatal("nil authenticator allowed request") })(ctx)
		if ctx.Response.StatusCode() != 401 || string(ctx.Response.Header.Peek("Cache-Control")) != "no-store" || string(ctx.Response.Header.Peek("WWW-Authenticate")) != "Bearer" {
			t.Fatalf("invalid authentication response: %s", ctx.Response.Header.String())
		}
	}
	calls := 0
	h := Auth(func(token string) bool { calls++; return token == "valid" })(func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(204) })
	empty := requestContext(t, "/", map[string]string{"Authorization": "bearer    "})
	h(empty)
	if calls != 0 {
		t.Fatal("empty bearer reached authenticator")
	}
	valid := requestContext(t, "/", map[string]string{"Authorization": "bEaReR  valid "})
	h(valid)
	if calls != 1 || valid.Response.StatusCode() != 204 {
		t.Fatal("case-insensitive bearer was rejected")
	}
}

func TestCORSCopiesOriginConfiguration(t *testing.T) {
	origins := []string{"https://allowed.test"}
	h := CORS(origins, nil, nil)(func(*fasthttp.RequestCtx) {})
	origins[0] = "https://changed.test"
	for _, origin := range []string{"https://allowed.test", "https://changed.test", ""} {
		ctx := requestContext(t, "/", map[string]string{"Origin": origin})
		h(ctx)
		allowed := string(ctx.Response.Header.Peek("Access-Control-Allow-Origin"))
		if (allowed == origin && origin != "") != (origin == "https://allowed.test") || !strings.Contains(joinHeaderValues(ctx.Response.Header.PeekAll("Vary")), "Origin") {
			t.Fatalf("origin=%q allowed=%q vary=%q", origin, allowed, ctx.Response.Header.Peek("Vary"))
		}
	}
}

func TestCacheLRURetainsRecentlyReadEntry(t *testing.T) {
	c := newBoundedCache[string, string](2, 20)
	expires := time.Now().Add(time.Minute)
	defer func() {
		for _, key := range []string{"a", "b", "c", "large"} {
			c.delete(key)
		}
	}()
	c.put("a", "a", 10, expires)
	c.put("b", "b", 10, expires)
	c.get("a", time.Now())
	c.put("c", "c", 10, expires)
	if _, ok := c.get("b", time.Now()); ok {
		t.Fatal("least recently read entry survived eviction")
	}
	c.put("a", "updated", 10, expires)
	if got, _ := c.get("a", time.Now()); got != "updated" {
		t.Fatal("full cache lost replacement")
	}
	c.put("large", "large", 21, expires)
	if _, ok := c.get("large", time.Now()); ok {
		t.Fatal("byte budget exceeded")
	}
}

func TestCacheVariantsUseOriginalRequestHeaders(t *testing.T) {
	calls := 0
	h := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.Response.Header.Set("Vary", "Accept-Language")
		ctx.SetBody(ctx.Request.Header.Peek("Accept-Language"))
		ctx.Request.Header.Set("Accept-Language", "mutated")
	})
	for _, language := range []string{"en", "zh", "en", "zh"} {
		ctx := requestContext(t, "/", map[string]string{"Accept-Language": language})
		h(ctx)
		if string(ctx.Response.Body()) != language {
			t.Fatal("handler mutation changed variant identity")
		}
	}
	if calls != 2 {
		t.Fatalf("variant cache missed: calls=%d", calls)
	}
}

func TestCacheVariantSchemaChangesAndTinyBudget(t *testing.T) {
	for _, entries := range []int{1, 2, 10} {
		calls := 0
		h := CacheWithConfig(CacheConfig{Duration: time.Minute, MaxEntries: entries, MaxBytes: 4096})(func(ctx *fasthttp.RequestCtx) {
			calls++
			field := "X-A"
			if calls > 1 {
				field = "X-B"
			}
			ctx.Response.Header.Set("Vary", field)
			ctx.SetBody(ctx.Request.Header.Peek(field))
		})
		for _, item := range []struct{ a, b, want string }{{"a", "old", "a"}, {"new", "b", "b"}, {"a", "b", "b"}} {
			ctx := requestContext(t, "/", map[string]string{"X-A": item.a, "X-B": item.b})
			h(ctx)
			if string(ctx.Response.Body()) != item.want {
				t.Fatalf("entries=%d stale schema returned %q", entries, ctx.Response.Body())
			}
		}
	}
}

func TestStaticConditionalAndHeadResponses(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "asset.txt")
	if err := os.WriteFile(file, []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := os.Chtimes(file, modified, modified); err != nil {
		t.Fatal(err)
	}
	h := Static(root)(func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(404) })
	for _, method := range []string{"GET", "HEAD"} {
		ctx := requestContext(t, "/asset.txt", map[string]string{"If-Modified-Since": modified.UTC().Format(http.TimeFormat)})
		ctx.Request.Header.SetMethod(method)
		h(ctx)
		if ctx.Response.StatusCode() != 304 || ctx.Response.IsBodyStream() || len(ctx.Response.Body()) != 0 {
			t.Fatalf("%s did not return an empty 304", method)
		}
	}
	head := requestContext(t, "/asset.txt", nil)
	head.Request.Header.SetMethod("HEAD")
	h(head)
	if head.Response.StatusCode() != 200 || head.Response.Header.ContentLength() != 5 || head.Response.IsBodyStream() || len(head.Response.Body()) != 0 {
		t.Fatal("HEAD read file body or lost its length")
	}
	conditional := requestContext(t, "/asset.txt", map[string]string{"If-Modified-Since": modified.UTC().Format(http.TimeFormat), "If-None-Match": `"unknown"`})
	h(conditional)
	if conditional.Response.StatusCode() != 200 || string(conditional.Response.Body()) != "asset" {
		t.Fatal("If-None-Match did not take precedence")
	}
}
