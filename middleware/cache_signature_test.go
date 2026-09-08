package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func cacheTestSignature(h *fasthttp.ResponseHeader) string {
	mac := hmac.New(sha256.New, []byte("test-only-signing-key"))
	fmt.Fprintf(mac, "\"x-request-id\": %s\n\"@signature-params\": (\"x-request-id\");keyid=\"test\";alg=\"hmac-sha256\"", h.Peek("X-Request-ID"))
	return "sig1=:" + base64.StdEncoding.EncodeToString(mac.Sum(nil)) + ":"
}

func TestCacheSignaturesRemainValidInBothOrders(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			defer cache.Close()
			calls := 0
			leaf := func(c *fasthttp.RequestCtx) { calls++; c.SetBodyString("signed response") }
			sign := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
				return func(c *fasthttp.RequestCtx) {
					next(c)
					c.Response.Header.Set("Signature-Input", `sig1=("x-request-id");keyid="test";alg="hmac-sha256"`)
					c.Response.Header.Set("Signature", cacheTestSignature(&c.Response.Header))
				}
			}
			h := cache.Middleware(RequestID()(sign(leaf)))
			if outside {
				h = RequestID()(sign(cache.Middleware(leaf)))
			}
			var previousID string
			for i := 0; i < 3; i++ {
				c := requestContext(t, "/signed", nil)
				h(c)
				id := string(c.Response.Header.Peek("X-Request-ID"))
				if id == "" || id == previousID {
					t.Fatal("request ID reused or missing")
				}
				previousID = id
				if string(c.Response.Header.Peek("Signature")) != cacheTestSignature(&c.Response.Header) {
					t.Fatal("response signature no longer covers the current request ID")
				}
				if outside && i > 0 && string(c.Response.Header.Peek("X-Cache")) != "HIT" {
					t.Fatal("signing outside Cache should still allow hits")
				}
				if !outside && (len(c.Response.Header.Peek("X-Cache")) != 0 || len(c.Response.Header.Peek("Age")) != 0) {
					t.Fatal("cache changed a signed response")
				}
			}
			want := 3
			if outside {
				want = 1
			}
			if calls != want {
				t.Fatalf("origin calls = %d, want %d", calls, want)
			}
		})
	}
}

func TestCacheBypassesSignatureFieldsAndTrailers(t *testing.T) {
	for _, field := range []string{"Signature", "Signature-Input", "sIgNaTuRe", "sIgNaTuRe-InPuT"} {
		for _, trailer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trailer=%v", field, trailer), func(t *testing.T) {
				cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
				defer cache.Close()
				calls := 0
				h := cache.Middleware(func(c *fasthttp.RequestCtx) {
					calls++
					c.Response.Header.DisableNormalizing()
					if trailer {
						if err := c.Response.Header.SetTrailer(field); err != nil {
							t.Fatal(err)
						}
					} else {
						// Presence is significant even for an empty/malformed value.
						c.Response.Header.Set(field, "")
					}
					c.SetBodyString("protected")
				})
				for i := 0; i < 2; i++ {
					c := requestContext(t, "/", nil)
					h(c)
					if !hasResponseSignature(&c.Response.Header) || string(c.Response.Body()) != "protected" {
						t.Fatal("signature metadata or body was discarded")
					}
				}
				if calls != 2 || len(cache.cache.items) != 0 {
					t.Fatal("signed response or signature trailer entered the cache")
				}
			})
		}
	}
}

func TestCachePreservesSignatureSetBeforeLookup(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	defer cache.Close()
	calls := 0
	h := cache.Middleware(func(c *fasthttp.RequestCtx) { calls++; c.SetBodyString("public") })
	h(requestContext(t, "/", nil))
	c := requestContext(t, "/", nil)
	c.Response.Header.Set("Signature", "already-signed")
	h(c)
	if calls != 2 || string(c.Response.Header.Peek("Signature")) != "already-signed" || len(c.Response.Header.Peek("X-Cache")) != 0 {
		t.Fatal("cache rewrote an already signed response on a warm lookup")
	}
}

func TestCacheBodyDigestStillCacheable(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	defer cache.Close()
	body := strings.Repeat("protected body", 32)
	sum := sha256.Sum256([]byte(body))
	digest := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	calls := 0
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		calls++
		c.Response.Header.Set("Content-Digest", digest)
		c.SetBodyString(body)
	})
	for i := 0; i < 2; i++ {
		c := requestContext(t, "/", nil)
		h(c)
		if string(c.Response.Header.Peek("Content-Digest")) != digest || sha256.Sum256(c.Response.Body()) != sum {
			t.Fatal("cached body digest changed")
		}
	}
	if calls != 1 {
		t.Fatal("body digest unnecessarily disabled caching")
	}
}
