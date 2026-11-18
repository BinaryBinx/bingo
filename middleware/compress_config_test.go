package middleware

import (
	"bytes"
	"strings"
	"testing"

	"github.com/klauspost/compress/gzip"
	"github.com/valyala/fasthttp"
)

func TestCompressAutomaticLargeJSONLevel(t *testing.T) {
	encode := func(media, body string, level int) []byte {
		t.Helper()
		c := requestContext(t, "/", map[string]string{"Accept-Encoding": "gzip"})
		defer c.Response.Reset()
		CompressWithConfig(CompressConfig{Level: level})(func(c *fasthttp.RequestCtx) {
			c.SetContentType(media)
			c.SetBodyString(body)
		})(c)
		decoded, err := c.Response.BodyGunzip()
		if err != nil || string(decoded) != body {
			t.Fatalf("gzip round trip failed: %v", err)
		}
		return bytes.Clone(c.Response.Body())
	}
	large := strings.Repeat(`{"name":"large","state":"ready"}`, 1800)
	for _, test := range []struct {
		media, body string
		wantLevel   int
	}{
		{"Application/Problem+JSON; charset=utf-8", large, gzip.BestSpeed},
		{"application/json", large[:4096], gzip.DefaultCompression},
		{"text/plain", large, gzip.DefaultCompression},
	} {
		automatic := encode(test.media, test.body, 0)
		explicit := encode(test.media, test.body, test.wantLevel)
		if !bytes.Equal(automatic, explicit) {
			t.Errorf("automatic gzip level differs from %d for %s (%d bytes)", test.wantLevel, test.media, len(test.body))
		}
	}
}

func TestCompressContentTypePolicy(t *testing.T) {
	for _, item := range []struct {
		media      string
		compressed bool
	}{
		{"text/html; charset=utf-8", true}, {"APPLICATION/JSON; charset=UTF-8", true},
		{"application/problem+json", true}, {"application/atom+xml", true}, {"image/svg+xml", true},
		{"image/png", false}, {"application/octet-stream", false}, {"application/zip", false}, {"video/mp4", false},
	} {
		t.Run(item.media, func(t *testing.T) {
			body := strings.Repeat("payload ", 1024)
			h := Compress()(func(c *fasthttp.RequestCtx) { c.SetContentType(item.media); c.SetBodyString(body) })
			c := requestContext(t, "/", map[string]string{"Accept-Encoding": "gzip"})
			h(c)
			compressed := string(c.Response.Header.Peek("Content-Encoding")) == "gzip"
			if compressed != item.compressed {
				t.Fatalf("gzip=%v", compressed)
			}
			if compressed {
				decoded, err := c.Response.BodyGunzip()
				if err != nil || string(decoded) != body {
					t.Fatalf("gzip roundtrip: %v", err)
				}
			} else if string(c.Response.Body()) != body {
				t.Fatal("skipped response changed")
			}
		})
	}
}

func TestCompressConfigOverrides(t *testing.T) {
	for _, level := range []int{gzip.HuffmanOnly, gzip.BestSpeed, gzip.DefaultCompression, gzip.BestCompression, 0, 100, -100} {
		cfg := CompressConfig{Level: level, MinSize: 8, ContentTypes: []string{"application/octet-stream"}}
		h := CompressWithConfig(cfg)(func(c *fasthttp.RequestCtx) {
			c.SetContentType("application/octet-stream")
			c.SetBodyString(strings.Repeat("a", 200))
		})
		cfg.ContentTypes[0] = "image/png"
		c := requestContext(t, "/", map[string]string{"Accept-Encoding": "gzip"})
		h(c)
		decoded, err := c.Response.BodyGunzip()
		if string(c.Response.Header.Peek("Content-Encoding")) != "gzip" || err != nil || string(decoded) != strings.Repeat("a", 200) {
			t.Fatalf("level %d failed: %v", level, err)
		}
	}
	for _, cfg := range []CompressConfig{{Disabled: true}, {ContentTypes: []string{}}, {MinSize: 10000}, {Skip: func(c *fasthttp.RequestCtx) bool { return string(c.Path()) == "/download" }}} {
		c := requestContext(t, "/download", map[string]string{"Accept-Encoding": "gzip"})
		h := CompressWithConfig(cfg)(func(c *fasthttp.RequestCtx) {
			c.Request.SetRequestURI("/changed")
			c.SetBodyString(strings.Repeat("a", 1000))
		})
		h(c)
		if len(c.Response.Header.Peek("Content-Encoding")) != 0 || len(c.Response.Body()) != 1000 {
			t.Fatal("disabled, skipped or undersized response compressed")
		}
		if (cfg.Disabled || cfg.Skip != nil) && len(c.Response.Header.Peek("Vary")) != 0 {
			t.Fatal("bypass added Vary")
		}
	}
	c := requestContext(t, "/", map[string]string{"Accept-Encoding": "gzip"})
	CompressWithConfig(CompressConfig{ContentTypes: []string{"*/*"}})(func(c *fasthttp.RequestCtx) {
		c.SetContentType("custom/format")
		c.SetBodyString(strings.Repeat("a", 1000))
	})(c)
	if string(c.Response.Header.Peek("Content-Encoding")) != "gzip" {
		t.Fatal("wildcard override failed")
	}
}
