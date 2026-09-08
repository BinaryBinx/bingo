package middleware

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestStaticPreconditionsAcrossStoragePaths(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("asset", 20)
	writeStaticFixture(t, root, body)
	modified := time.Date(2026, 1, 2, 3, 4, 5, 900000000, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "asset.txt"), modified, modified); err != nil {
		t.Fatal(err)
	}
	before := modified.Add(-time.Second).Format(http.TimeFormat)
	same := modified.Format(http.TimeFormat)
	after := modified.Add(time.Second).Format(http.TimeFormat)
	cases := []struct {
		name    string
		headers [][2]string
		status  int
	}{
		{"ordinary", nil, 200},
		{"match-wildcard", [][2]string{{"If-Match", " \t* "}}, 200},
		{"match-tag-without-etag", [][2]string{{"If-Match", `"unknown"`}}, 412},
		{"match-weak-tag", [][2]string{{"If-Match", `W/"unknown"`}}, 412},
		{"match-tag-list", [][2]string{{"If-Match", `"one", "two"`}}, 412},
		{"match-repeated-tags", [][2]string{{"If-Match", `"one"`}, {"If-Match", `"two"`}}, 412},
		{"match-suppresses-date", [][2]string{{"If-Match", "*"}, {"If-Unmodified-Since", before}}, 200},
		{"match-before-none-match", [][2]string{{"If-Match", `"unknown"`}, {"If-None-Match", "*"}}, 412},
		{"modified-after-required-date", [][2]string{{"If-Unmodified-Since", before}}, 412},
		{"same-second-unmodified", [][2]string{{"If-Unmodified-Since", same}}, 200},
		{"future-unmodified", [][2]string{{"If-Unmodified-Since", after}}, 200},
		{"invalid-unmodified-date", [][2]string{{"If-Unmodified-Since", "invalid"}}, 200},
		{"repeated-unmodified-date", [][2]string{{"If-Unmodified-Since", before}, {"If-Unmodified-Since", before}}, 200},
		{"unmodified-before-none-match", [][2]string{{"If-Unmodified-Since", before}, {"If-None-Match", "*"}}, 412},
		{"none-match-wildcard", [][2]string{{"If-None-Match", " \t* "}}, 304},
		{"none-match-tag-without-etag", [][2]string{{"If-None-Match", `"unknown"`}}, 200},
		{"none-match-suppresses-date", [][2]string{{"If-None-Match", `W/"one", "two"`}, {"If-Modified-Since", after}}, 200},
		{"empty-none-match-suppresses-date", [][2]string{{"If-None-Match", ""}, {"If-Modified-Since", after}}, 200},
		{"none-match-before-date", [][2]string{{"If-None-Match", "*"}, {"If-Modified-Since", before}}, 304},
		{"same-second-modified", [][2]string{{"If-Modified-Since", same}}, 304},
		{"older-modified-date", [][2]string{{"If-Modified-Since", before}}, 200},
		{"invalid-modified-date", [][2]string{{"If-Modified-Since", "invalid"}}, 200},
		{"repeated-modified-date", [][2]string{{"If-Modified-Since", after}, {"If-Modified-Since", after}}, 200},
	}
	for _, mode := range []string{"legacy-uncached", "mutable-snapshot", "immutable-snapshot", "immutable-stream"} {
		t.Run(mode, func(t *testing.T) {
			next := func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) }
			var h fasthttp.RequestHandler
			if mode == "legacy-uncached" {
				h = StaticWithConfig(root, StaticConfig{TTL: -1})(next)
			} else {
				cfg := StaticConfig{Immutable: strings.HasPrefix(mode, "immutable")}
				if mode == "immutable-stream" {
					cfg.MaxEntryBytes = 8
				}
				s, err := NewStaticHandler(root, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				h = s.Middleware(next)
			}
			warm := requestContext(t, "/asset.txt", nil)
			h(warm)
			warm.Response.Reset()
			for _, method := range []string{"GET", "HEAD"} {
				for _, tc := range cases {
					t.Run(method+"/"+tc.name, func(t *testing.T) {
						c := requestContext(t, "/asset.txt", nil)
						c.Request.Header.SetMethod(method)
						for _, header := range tc.headers {
							c.Request.Header.Add(header[0], header[1])
						}
						h(c)
						if c.Response.StatusCode() != tc.status {
							t.Fatalf("status=%d, want %d", c.Response.StatusCode(), tc.status)
						}
						if got := string(c.Response.Header.Peek("Last-Modified")); got != same {
							t.Fatalf("Last-Modified=%q, want %q", got, same)
						}
						if tc.status != 200 || method == "HEAD" {
							if c.Response.IsBodyStream() || len(c.Response.Body()) != 0 {
								t.Fatal("conditional/HEAD response read or retained a body")
							}
						} else if got := string(c.Response.Body()); got != body {
							t.Fatalf("body=%q", got)
						}
						if method == "HEAD" && tc.status == 200 && c.Response.Header.ContentLength() != len(body) {
							t.Fatal("HEAD lost representation length")
						}
					})
				}
				missing := requestContext(t, "/missing.txt", map[string]string{"If-Match": "*", "If-None-Match": "*"})
				missing.Request.Header.SetMethod(method)
				h(missing)
				if missing.Response.StatusCode() != 404 {
					t.Fatal("preconditions overrode missing-file response")
				}
			}
		})
	}
}

func TestStaticImmutableStreamLengthAndOwnership(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("large file data\n", 8192)
	writeStaticFixture(t, root, body)
	for _, managed := range []bool{false, true} {
		for _, immutable := range []bool{false, true} {
			t.Run(fmt.Sprintf("managed=%v/immutable=%v", managed, immutable), func(t *testing.T) {
				cfg := StaticConfig{Immutable: immutable, MaxEntryBytes: 16}
				next := func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) }
				h := StaticWithConfig(root, cfg)(next)
				if managed {
					s, err := NewStaticHandler(root, cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					h = s.Middleware(next)
					// Close/reload happen after opening the response descriptor below.
					original := h
					h = func(c *fasthttp.RequestCtx) {
						original(c)
						if err := s.Reload(""); err != nil {
							t.Fatal(err)
						}
						if err := s.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
				c := requestContext(t, "/asset.txt", nil)
				h(c)
				f, ok := c.Response.BodyStream().(*os.File)
				if !ok {
					t.Fatal("stream wrapper hides the file from fasthttp's sendfile detection")
				}
				wantLength := -1
				if immutable {
					wantLength = len(body)
				}
				if got := c.Response.Header.ContentLength(); got != wantLength {
					t.Fatalf("stream length=%d, want %d", got, wantLength)
				}
				var wire bytes.Buffer
				writer := bufio.NewWriter(&wire)
				if err := c.Response.Write(writer); err != nil {
					t.Fatal(err)
				}
				if err := writer.Flush(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("sent file descriptor remains open: %v", err)
				}
				var decoded fasthttp.Response
				defer decoded.Reset()
				if err := decoded.Read(bufio.NewReader(&wire)); err != nil {
					t.Fatal(err)
				}
				if got := string(decoded.Body()); got != body {
					t.Fatalf("wire body length=%d, want %d", len(got), len(body))
				}
				if immutable && len(decoded.Header.Peek("Transfer-Encoding")) != 0 {
					t.Fatal("immutable response used chunked framing")
				}
			})
		}
	}
}

func TestStaticPreconditionsClosePreviousStream(t *testing.T) {
	root := t.TempDir()
	writeStaticFixture(t, root, "asset")
	h := Static(root)(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) })
	for _, header := range []string{"If-Match", "If-None-Match"} {
		f, err := os.Open(filepath.Join(root, "asset.txt"))
		if err != nil {
			t.Fatal(err)
		}
		value := `"unknown"`
		if header == "If-None-Match" {
			value = "*"
		}
		c := requestContext(t, "/asset.txt", map[string]string{header: value})
		c.SetBodyStream(f, 5)
		h(c)
		if c.Response.IsBodyStream() || len(c.Response.Body()) != 0 || c.Response.Header.ContentLength() > 0 {
			t.Fatal("conditional result retained the previous representation")
		}
		if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("conditional result leaked the previous stream: %v", err)
		}
	}
}
