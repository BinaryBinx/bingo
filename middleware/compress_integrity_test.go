package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCompressPreservesIntegrityMetadata(t *testing.T) {
	body := []byte(strings.Repeat("protected content", 256))
	sum := sha256.Sum256(body)
	digest := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	for _, header := range []string{"Content-Digest", "Repr-Digest", "Digest", "Content-MD5", "Signature", "Signature-Input"} {
		for _, accept := range []string{"gzip", "identity"} {
			t.Run(header+"/"+accept, func(t *testing.T) {
				var before []byte
				h := CompressWithConfig(CompressConfig{ContentTypes: []string{"*/*"}})(func(c *fasthttp.RequestCtx) {
					c.SetBody(body)
					c.Response.Header.Set(header, digest)
					c.Response.Header.Set("Vary", "Origin")
					c.Response.Header.Set("ETag", `"protected"`)
					c.Response.Header.SetContentLength(len(body))
					before = append([]byte(nil), c.Response.Header.Header()...)
				})
				c := requestContext(t, "/asset", map[string]string{"Accept-Encoding": accept})
				h(c)
				if !bytes.Equal(c.Response.Body(), body) || sha256.Sum256(c.Response.Body()) != sum {
					t.Fatal("compression changed integrity-protected content")
				}
				if after := c.Response.Header.Header(); !bytes.Equal(before, after) {
					t.Fatalf("compression changed potentially signed headers:\nbefore %s\nafter %s", before, after)
				}
			})
		}
	}
}

func TestCompressPreservesDeclaredIntegrityTrailers(t *testing.T) {
	for _, header := range []string{"content-digest", "repr-digest", "digest", "signature", "signature-input"} {
		t.Run(header, func(t *testing.T) {
			h := Compress()(func(c *fasthttp.RequestCtx) {
				c.SetBodyString(strings.Repeat("content", 100))
				if err := c.Response.Header.SetTrailer(header); err != nil {
					t.Fatal(err)
				}
			})
			c := requestContext(t, "/", map[string]string{"Accept-Encoding": "gzip"})
			h(c)
			if len(c.Response.Header.Peek("Content-Encoding")) != 0 || len(c.Response.Header.Peek("Vary")) != 0 {
				t.Fatal("response with integrity trailers was transformed")
			}
			if len(c.Response.Header.PeekTrailerKeys()) != 1 {
				t.Fatal("integrity trailer declaration was removed")
			}
		})
	}
}

func TestCompressProtectsNonCanonicalAndEmptyIntegrityFields(t *testing.T) {
	for _, value := range []string{"", "sha-256=:dGVzdA==:"} {
		h := Compress()(func(c *fasthttp.RequestCtx) {
			c.Response.Header.DisableNormalizing()
			c.Response.Header.Set("cOnTeNt-DiGeSt", value)
			c.SetBodyString(strings.Repeat("content", 100))
		})
		c := requestContext(t, "/", map[string]string{"Accept-Encoding": "gzip"})
		h(c)
		if len(c.Response.Header.Peek("Content-Encoding")) != 0 || string(c.Response.Header.Peek("cOnTeNt-DiGeSt")) != value {
			t.Fatal("integrity field was ignored or removed")
		}
	}
}

func TestCompressIntegritySurvivesCacheBothOrders(t *testing.T) {
	for _, outerCache := range []bool{true, false} {
		calls := 0
		body := []byte(strings.Repeat("public protected response", 100))
		sum := sha256.Sum256(body)
		digest := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
		leaf := func(c *fasthttp.RequestCtx) {
			calls++
			c.SetBody(body)
			c.Response.Header.Set("Content-Digest", digest)
		}
		h := Cache(time.Minute)(Compress()(leaf))
		if !outerCache {
			h = Compress()(Cache(time.Minute)(leaf))
		}
		for range 3 {
			c := requestContext(t, "/asset", map[string]string{"Accept-Encoding": "gzip"})
			h(c)
			if sha256.Sum256(c.Response.Body()) != sum || string(c.Response.Header.Peek("Content-Digest")) != digest || len(c.Response.Header.Peek("Content-Encoding")) > 0 {
				t.Fatal("cache/compress stack served an invalid digest")
			}
		}
		if calls != 1 {
			t.Fatalf("protected public response was not cached: %d calls", calls)
		}
	}
}
