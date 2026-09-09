package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BinaryBinx/bingo/middleware"
	"github.com/valyala/fasthttp"
)

func TestRouterErrorsPreservePublicHeadersAndReplaceBody(t *testing.T) {
	app := reviewLatestApp(t)
	app.GET("/exists", func(c *RequestContext) { c.String(200, "ok") })
	app.Use(middleware.CORS([]string{"https://client.test"}, nil, nil))
	app.Use(middleware.RequestID())
	app.Use(middleware.Security())
	for _, normalize := range []bool{true, false} {
		for _, tc := range []struct {
			method, uri string
			status      int
		}{
			{"GET", "/missing", 404}, {"HEAD", "/missing", 404}, {"POST", "/exists", 405},
		} {
			t.Run(fmt.Sprintf("%t/%s%s", normalize, tc.method, tc.uri), func(t *testing.T) {
				var c fasthttp.RequestCtx
				defer c.Response.Reset()
				c.Request.SetRequestURI("http://example.test" + tc.uri)
				c.Request.Header.SetMethod(tc.method)
				c.Request.Header.Set("Origin", "https://client.test")
				if !normalize {
					c.Response.Header.DisableNormalizing()
				}
				stale := []string{"Content-Encoding", "Content-Digest", "Signature", "ETag", "Content-Range", "Content-Location", "Set-Cookie", "Age"}
				for _, name := range stale {
					if !normalize {
						name = strings.ToLower(name)
					}
					c.Response.Header.Set(name, "old")
				}
				c.Response.Header.Set("Content-Type", "application/json")
				c.Response.Header.Set("Cache-Control", "public, max-age=60")
				c.Response.Header.AddTrailer("X-Checksum")
				c.Response.Header.Set("X-Checksum", "old")
				if tc.status == 405 {
					c.Response.Header.AddTrailer("Allow")
				}
				stream := &jsonBodyStream{Reader: strings.NewReader("old body")}
				c.SetBodyStream(stream, -1)
				app.handleRequest(&c)
				if c.Response.StatusCode() != tc.status || string(c.Response.Body()) != fasthttp.StatusMessage(tc.status) {
					t.Fatalf("unexpected error: %d %q", c.Response.StatusCode(), c.Response.Body())
				}
				if !stream.closed || stream.reads != 0 || c.Response.IsBodyStream() {
					t.Fatal("router consumed or retained the old response stream")
				}
				for name, want := range map[string]string{
					"Access-Control-Allow-Origin": "https://client.test",
					"X-Content-Type-Options":      "nosniff",
					"Content-Type":                "text/plain; charset=utf-8",
					"Cache-Control":               "no-store",
				} {
					if got := string(c.Response.Header.Peek(name)); got != want {
						t.Errorf("%s=%q want %q", name, got, want)
					}
				}
				if len(c.Response.Header.Peek("X-Request-ID")) == 0 {
					t.Error("lost request ID")
				}
				for key := range c.Response.Header.All() {
					for _, name := range append(stale, "Trailer", "X-Checksum") {
						if strings.EqualFold(string(key), name) {
							t.Errorf("retained %s", key)
						}
					}
				}
				if len(c.Response.Header.PeekTrailerKeys()) != 0 {
					t.Error("retained trailers")
				}
				if tc.status == 405 && !strings.Contains(string(c.Response.Header.Peek("Allow")), "GET") {
					t.Error("405 lost the permitted GET method")
				}
			})
		}
	}
}
