package middleware

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestStaticFallbackPreservesOriginalResponse(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, replacement := range []string{"missing", "directory"} {
			for _, existing := range []bool{false, true} {
				t.Run(fmt.Sprintf("managed=%t/%s/existing=%t", managed, replacement, existing), func(t *testing.T) {
					root := t.TempDir()
					file := filepath.Join(root, "asset.json")
					if err := os.WriteFile(file, []byte(`{"file":true}`), 0600); err != nil {
						t.Fatal(err)
					}
					cfg := StaticConfig{ETag: func(os.FileInfo) string {
						// Exercise both an Open failure and a non-regular replacement
						// after Stat, without relying on filesystem race timing.
						if err := os.Remove(file); err != nil {
							t.Fatal(err)
						}
						if replacement == "directory" {
							if err := os.Mkdir(file, 0700); err != nil {
								t.Fatal(err)
							}
						}
						return `"file-v1"`
					}}
					c := requestContext(t, "/asset.json", nil)
					c.SetStatusCode(fasthttp.StatusAccepted)
					c.SetBodyString("old")
					c.Response.Header.Set("X-Request-ID", "outer-id")
					if existing {
						for _, field := range [][2]string{{"Content-Type", "application/custom"}, {"ETag", `"outer"`}, {"Last-Modified", "Fri, 02 Jan 2026 03:04:05 GMT"}, {"Accept-Ranges", "none"}, {"Content-Range", "bytes 0-2/3"}, {"Content-Length", "3"}} {
							c.Response.Header.Set(field[0], field[1])
						}
					}
					before := make(map[string]string)
					for _, field := range []string{"Content-Type", "Content-Length", "ETag", "Last-Modified", "Accept-Ranges", "Content-Range", "X-Request-ID"} {
						before[field] = string(c.Response.Header.Peek(field))
					}
					called := false
					next := func(c *fasthttp.RequestCtx) {
						called = true
						for field, want := range before {
							if got := string(c.Response.Header.Peek(field)); got != want {
								t.Errorf("fallback %s=%q, want original %q", field, got, want)
							}
						}
						if c.Response.StatusCode() != fasthttp.StatusAccepted || string(c.Response.Body()) != "old" {
							t.Error("fallback received a modified status or body")
						}
						c.SetStatusCode(fasthttp.StatusOK)
						c.SetContentType("application/json")
						c.SetBodyString(`{"dynamic":true}`)
					}
					var h fasthttp.RequestHandler
					if managed {
						files, err := NewStaticHandler(root, cfg)
						if err != nil {
							t.Fatal(err)
						}
						defer files.Close()
						h = files.Middleware(next)
					} else {
						h = StaticWithConfig(root, cfg)(next)
					}
					h(c)
					if !called || string(c.Response.Body()) != `{"dynamic":true}` {
						t.Fatal("dynamic fallback did not complete")
					}
				})
			}
		}
	}
}
