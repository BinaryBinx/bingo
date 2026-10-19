package middleware

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCacheNonCanonicalResponseControls(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name      string
		headers   [][2]string
		cacheable bool
	}{
		{"private", [][2]string{{"cache-control", "private"}}, false},
		{"no-store", [][2]string{{"cache-control", "no-store"}}, false},
		{"no-cache", [][2]string{{"cAcHe-CoNtRoL", "no-cache"}}, false},
		{"mixed-private", [][2]string{{"Cache-Control", "public, max-age=60"}, {"cache-control", "private"}}, false},
		{"vary-star", [][2]string{{"Vary", "Accept-Language"}, {"vary", "*"}}, false},
		{"set-cookie", [][2]string{{"set-cookie", "session=private"}}, false},
		{"connection", [][2]string{{"cOnNeCtIoN", "X-Connection-Token"}, {"X-Connection-Token", "private"}}, false},
		{"stale-age", [][2]string{{"cache-control", "max-age=60"}, {"age", "120"}}, false},
		{"stale-date", [][2]string{{"cache-control", "max-age=60"}, {"date", now.Add(-2 * time.Minute).Format(http.TimeFormat)}}, false},
		{"expired", [][2]string{{"expires", now.Add(-time.Minute).Format(http.TimeFormat)}}, false},
		{"duplicate-age", [][2]string{{"Age", "1"}, {"age", "2"}}, false},
		{"duplicate-date", [][2]string{{"Date", now.Format(http.TimeFormat)}, {"date", now.Format(http.TimeFormat)}}, false},
		{"duplicate-expires", [][2]string{{"Expires", now.Add(time.Minute).Format(http.TimeFormat)}, {"expires", now.Add(2 * time.Minute).Format(http.TimeFormat)}}, false},
		{"duplicate-max-age", [][2]string{{"Cache-Control", "max-age=60"}, {"cache-control", "max-age=120"}}, false},
		{"shared-freshness", [][2]string{{"cache-control", "max-age=0, s-maxage=100"}, {"age", "10"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			calls := 0
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				calls++
				c.Response.Header.DisableNormalizing()
				// Parse an upstream response: fasthttp's setters deliberately
				// ignore Date, whereas parsed headers retain its supplied values.
				var wire strings.Builder
				wire.WriteString("HTTP/1.1 200 OK\r\n")
				for _, field := range tc.headers {
					fmt.Fprintf(&wire, "%s: %s\r\n", field[0], field[1])
				}
				wire.WriteString("Content-Length: 0\r\n\r\n")
				if err := c.Response.Header.Read(bufio.NewReader(strings.NewReader(wire.String()))); err != nil {
					t.Fatal(err)
				}
				c.SetBodyString(fmt.Sprintf("response-%d", calls))
			})
			h(requestContext(t, "http://example.test/policy", nil))
			second := requestContext(t, "http://example.test/policy", nil)
			h(second)
			hit := string(second.Response.Header.Peek("X-Cache")) == "HIT"
			wantCalls := 2
			if tc.cacheable {
				wantCalls = 1
				if string(second.Response.Header.Peek("Age")) != "10" {
					t.Error("lowercase Age or s-maxage precedence was lost")
				}
			}
			if hit != tc.cacheable || calls != wantCalls {
				t.Fatalf("hit=%t calls=%d body=%q, want hit=%t calls=%d", hit, calls, second.Response.Body(), tc.cacheable, wantCalls)
			}
		})
	}
}

func TestCacheNonCanonicalVaryAndRepeatedHeaders(t *testing.T) {
	for _, normalizeHit := range []bool{true, false} {
		t.Run(fmt.Sprint(normalizeHit), func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			calls := 0
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				calls++
				c.Response.Header.DisableNormalizing()
				c.Response.Header.Add("vary", "Accept-Language")
				c.Response.Header.Add("VARY", "X-Device")
				c.Response.Header.Add("link", "</a>; rel=preload")
				c.Response.Header.Add("Link", "</b>; rel=preload")
				c.SetBodyString(string(c.Request.Header.Peek("Accept-Language")) + "/" + string(c.Request.Header.Peek("X-Device")))
			})
			for round := range 2 {
				for _, variant := range [][2]string{{"en", "phone"}, {"zh", "phone"}, {"en", "desktop"}} {
					c := requestContext(t, "http://example.test/variant", map[string]string{"Accept-Language": variant[0], "X-Device": variant[1]})
					if !normalizeHit {
						c.Response.Header.DisableNormalizing()
					}
					h(c)
					if got := string(c.Response.Body()); got != variant[0]+"/"+variant[1] {
						t.Fatalf("wrong representation %q for %v", got, variant)
					}
					if round == 1 && string(c.Response.Header.Peek("X-Cache")) != "HIT" {
						t.Fatal("warm variant was not cached")
					}
					var links []string
					c.Response.Header.VisitAll(func(key, value []byte) {
						if strings.EqualFold(string(key), "Link") {
							links = append(links, string(value))
						}
					})
					if strings.Join(links, "|") != "</a>; rel=preload|</b>; rel=preload" {
						t.Fatalf("mixed-case repeated fields lost: %v", links)
					}
				}
			}
			if calls != 3 {
				t.Fatalf("handler calls=%d, want one per variant", calls)
			}
		})
	}
}

func TestCacheNonCanonicalRequestID(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	defer cache.Close()
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		c.Response.Header.DisableNormalizing()
		c.Response.Header.Set("x-request-id", "origin-id")
		c.SetBodyString("public")
	})
	h(requestContext(t, "/id", nil))
	for _, outerID := range []string{"", "outer-id"} {
		c := requestContext(t, "/id", nil)
		c.Response.Header.DisableNormalizing()
		if outerID != "" {
			c.Response.Header.Set("x-request-id", outerID)
		}
		h(c)
		var ids []string
		c.Response.Header.VisitAll(func(key, value []byte) {
			if strings.EqualFold(string(key), "X-Request-ID") {
				ids = append(ids, string(value))
			}
		})
		if len(ids) != 1 || ids[0] == "origin-id" || ids[0] == "" || outerID != "" && ids[0] != outerID {
			t.Fatalf("request ID replayed, duplicated or replaced: %v", ids)
		}
	}
}

func TestCacheNonCanonicalLocationInvalidation(t *testing.T) {
	for _, field := range []string{"location", "content-location"} {
		t.Run(field, func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			old := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("old") })
			old(requestContext(t, "http://example.test/item", nil))
			write := cache.Middleware(func(c *fasthttp.RequestCtx) {
				c.Response.Header.DisableNormalizing()
				c.Response.Header.Set(field, "/item")
				c.SetStatusCode(fasthttp.StatusNoContent)
			})
			c := requestContext(t, "http://example.test/action", nil)
			c.Request.Header.SetMethod("POST")
			write(c)
			fresh := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("new") })
			read := requestContext(t, "http://example.test/item", nil)
			fresh(read)
			if string(read.Response.Body()) != "new" {
				t.Fatal("lowercase redirect target kept stale cache")
			}
		})
	}
}

func TestCacheNonCanonicalPolicyTrailers(t *testing.T) {
	for _, field := range [][2]string{{"cache-control", "no-store"}, {"vary", "*"}, {"aGe", "120"}, {"date", "Fri, 02 Jan 2026 03:04:05 GMT"}, {"expires", "Fri, 02 Jan 2026 03:04:05 GMT"}} {
		t.Run(field[0], func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			calls := 0
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				calls++
				c.Response.Header.DisableNormalizing()
				if err := c.Response.Header.SetTrailer(field[0]); err != nil {
					t.Fatal(err)
				}
				c.Response.Header.Set(field[0], field[1])
				c.SetBodyString("trailer-controlled")
			})
			h(requestContext(t, "/trailers", nil))
			second := requestContext(t, "/trailers", nil)
			h(second)
			if calls != 2 || string(second.Response.Header.Peek("X-Cache")) == "HIT" {
				t.Fatal("trailer policy was lost by shared caching")
			}
		})
	}
}
