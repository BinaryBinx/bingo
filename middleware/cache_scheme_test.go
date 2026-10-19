package middleware

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// Origin-form headers are identical across protocols, just as with a TLS
// listener. Only the effective URI scheme distinguishes these requests.
func schemeCacheRequest(t *testing.T, scheme, uri string) *fasthttp.RequestCtx {
	t.Helper()
	c := requestContext(t, uri, map[string]string{"Host": "example.test"})
	c.URI().SetScheme(scheme)
	return c
}

func TestCacheSchemesKeepSeparateVariantsAndInvalidateTogether(t *testing.T) {
	for _, invalidation := range []string{"explicit", "write"} {
		t.Run(invalidation, func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			revision, calls := 0, 0
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				if !c.IsGet() {
					c.SetStatusCode(fasthttp.StatusNoContent)
					return
				}
				calls++
				c.Response.Header.Set("Vary", "Accept-Language")
				c.SetBodyString(fmt.Sprintf("%s/%s/%d", c.URI().Scheme(), c.Request.Header.Peek("Accept-Language"), revision))
			})
			check := func() {
				for round := range 2 {
					for _, scheme := range []string{"http", "https"} {
						for _, language := range []string{"en", "zh"} {
							c := schemeCacheRequest(t, scheme, "/item?q=%31")
							// Alternate absolute/origin form without merging protocols,
							// or changing the exact escaped query target.
							if round == 0 {
								c = requestContext(t, scheme+"://example.test/item?q=%31", nil)
							}
							c.Request.Header.Set("Accept-Language", language)
							h(c)
							want := fmt.Sprintf("%s/%s/%d", scheme, language, revision)
							if string(c.Response.Body()) != want {
								t.Fatalf("body=%q, want %q", c.Response.Body(), want)
							}
							if round == 1 && string(c.Response.Header.Peek("X-Cache")) != "HIT" {
								t.Fatal("equivalent origin-form request missed its scheme's variant")
							}
						}
					}
				}
			}
			check()
			revision++
			if invalidation == "explicit" {
				if err := cache.Invalidate("EXAMPLE.TEST", "/item?q=%31"); err != nil {
					t.Fatal(err)
				}
			} else {
				c := schemeCacheRequest(t, "https", "/item?q=%31")
				c.Request.Header.SetMethod("PUT")
				h(c)
			}
			check()
			if calls != 8 {
				t.Fatalf("calls=%d, want four variants before and after invalidation", calls)
			}
		})
	}
}

func TestCacheSchemesDoNotShareFlights(t *testing.T) {
	for _, fields := range [][]string{nil, {}, {"Accept-Language"}} {
		t.Run(fmt.Sprintf("headers=%#v", fields), func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute, CoalesceHeaders: fields})
			defer cache.Close()
			entered, release := make(chan struct{}), make(chan struct{})
			h := cache.Middleware(func(c *fasthttp.RequestCtx) {
				if string(c.URI().Scheme()) == "http" {
					close(entered)
					<-release
				}
				c.SetBody(c.URI().Scheme())
			})
			httpRequest := schemeCacheRequest(t, "http", "/item")
			httpDone := make(chan struct{})
			go func() { h(httpRequest); close(httpDone) }()
			var once sync.Once
			defer func() { once.Do(func() { close(release) }); <-httpDone }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP origin did not start")
			}
			httpsRequest := schemeCacheRequest(t, "https", "/item")
			httpsDone := make(chan struct{})
			go func() { h(httpsRequest); close(httpsDone) }()
			defer func() { once.Do(func() { close(release) }); <-httpsDone }()
			select {
			case <-httpsDone:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTPS request waited behind an HTTP origin")
			}
			if string(httpsRequest.Response.Body()) != "https" {
				t.Fatal("HTTPS response used the HTTP representation")
			}
			once.Do(func() { close(release) })
			<-httpDone
			for _, scheme := range []string{"http", "https"} {
				c := schemeCacheRequest(t, scheme, "/item")
				h(c)
				if string(c.Response.Body()) != scheme || string(c.Response.Header.Peek("X-Cache")) != "HIT" {
					t.Fatal("independent flights did not publish separate snapshots")
				}
			}
		})
	}
}
