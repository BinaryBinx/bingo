package middleware

import (
	"fmt"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCacheVaryIndexCoversLongLivedVariants(t *testing.T) {
	for _, order := range [][2]string{{"long", "short"}, {"short", "long"}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				c.Response.Header.Set("Vary", "Accept-Language")
				c.Response.Header.Set("Cache-Control", "max-age=60")
				if string(c.Request.Header.Peek("Accept-Language")) == "short" {
					c.Response.Header.Set("Cache-Control", "max-age=1")
				}
				c.SetBody(c.Request.Header.Peek("Accept-Language"))
			})
			for _, language := range order {
				c := requestContext(t, "http://example.test/ttl", map[string]string{"Accept-Language": language})
				h(c)
			}
			key := responseCacheKey{host: "example.test", uri: "/ttl"}
			later := time.Now().Add(2 * time.Second)
			var headers fasthttp.RequestHeader
			for _, language := range []string{"long", "short"} {
				headers.Set("Accept-Language", language)
				item, hit := cachedResponse(cache.cache, key, &headers, later)
				if hit != (language == "long") || hit && string(item.body) != language {
					t.Errorf("variant %s after 2s: hit=%t body=%q", language, hit, item.body)
				}
			}
			if _, hit := cachedResponse(cache.cache, key, &headers, later.Add(time.Minute)); hit {
				t.Error("expired variants were served")
			}
			if err := cache.Invalidate("example.test", "/ttl"); err != nil {
				t.Fatal(err)
			}
			headers.Set("Accept-Language", "long")
			if _, hit := cachedResponse(cache.cache, key, &headers, later); hit {
				t.Error("extended index escaped invalidation")
			}
		})
	}
}

func TestCacheChangedVarySchemaDoesNotInheritOldExpiry(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	defer cache.Close()
	changed := false
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		if changed {
			c.Response.Header.Set("Vary", "X-Device")
			c.Response.Header.Set("Cache-Control", "max-age=1")
			c.SetBodyString("new schema")
		} else {
			c.Response.Header.Set("Vary", "Accept-Language")
			c.Response.Header.Set("Cache-Control", "max-age=60")
			c.SetBodyString("old schema")
		}
	})
	first := requestContext(t, "http://example.test/schema", map[string]string{"Accept-Language": "en"})
	h(first)
	changed = true
	second := requestContext(t, "http://example.test/schema", map[string]string{"Accept-Language": "fr", "X-Device": "mobile"})
	h(second)
	key := responseCacheKey{host: "example.test", uri: "/schema"}
	item, hit := cachedResponse(cache.cache, key, &second.Request.Header, time.Now())
	if !hit || string(item.body) != "new schema" {
		t.Fatalf("schema was not replaced: hit=%t body=%q", hit, item.body)
	}
	later := time.Now().Add(2 * time.Second)
	if _, hit := cache.cache.get(key, later); hit {
		t.Error("new schema inherited the old schema's longer lifetime")
	}
	if _, hit := cachedResponse(cache.cache, key, &first.Request.Header, later); hit {
		t.Error("changed schema reused an old representation")
	}
}
