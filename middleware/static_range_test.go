package middleware

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestStaticRangesAndETagsAcrossStoragePaths(t *testing.T) {
	root := t.TempDir()
	body := "0123456789abcdefghijklmnopqrstuvwxyz"
	file := filepath.Join(root, "asset.txt")
	os.WriteFile(file, []byte(body), 0600)
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(file, modified, modified); err != nil {
		t.Fatal(err)
	}
	const etag = "\"v1,opaque\""
	huge := strings.Repeat("9", 80)
	cases := []struct {
		name, span, condition string
		extra                 [][2]string
		status                int
		body                  string
	}{
		{"normal", "", "", nil, 200, body},
		{"bounded", "bytes=2-5", "", nil, 206, "2345"},
		{"open", "bytes=32-", "", nil, 206, "wxyz"},
		{"suffix", "bytes=-4", "", nil, 206, "wxyz"},
		{"beyond", "bytes=99-100", "", nil, 416, ""},
		{"suffix-zero", "bytes=-0", "", nil, 416, ""},
		{"huge-end", "bytes=1-" + huge, "", nil, 206, body[1:]},
		{"huge-start", "bytes=" + huge + "-", "", nil, 416, ""},
		{"huge-suffix", "bytes=-" + huge, "", nil, 206, body},
		{"backwards", "bytes=10-9", "", nil, 200, body},
		{"invalid", "bytes=x-y", "", nil, 200, body},
		{"unknown-unit", "items=0-1", "", nil, 200, body},
		{"multiple", "bytes=0-1,4-5", "", nil, 200, body},
		{"repeated", "bytes=0-1", "", [][2]string{{"Range", "bytes=4-5"}}, 200, body},
		{"if-range-tag", "bytes=2-5", etag, nil, 206, "2345"},
		{"if-range-weak", "bytes=2-5", "W/" + etag, nil, 200, body},
		{"if-range-miss", "bytes=2-5", "\"other\"", nil, 200, body},
		{"if-range-date", "bytes=2-5", modified.Format(http.TimeFormat), nil, 206, "2345"},
		{"if-range-future-date", "bytes=2-5", modified.Add(time.Hour).Format(http.TimeFormat), nil, 200, body},
		{"if-range-old-date", "bytes=2-5", modified.Add(-time.Hour).Format(http.TimeFormat), nil, 200, body},
		{"tag-before-range", "bytes=999-", "", [][2]string{{"If-None-Match", "\"miss\", W/" + etag}, {"If-Modified-Since", modified.Add(-time.Hour).Format(http.TimeFormat)}}, 304, ""},
		{"if-match-strong", "bytes=2-5", "", [][2]string{{"If-Match", etag}, {"If-Unmodified-Since", modified.Add(-time.Hour).Format(http.TimeFormat)}}, 206, "2345"},
		{"if-match-weak", "bytes=2-5", "", [][2]string{{"If-Match", "W/" + etag}}, 412, ""},
	}
	for _, mode := range []string{"legacy", "cold", "snapshot", "immutable-snapshot", "stream", "immutable-stream"} {
		t.Run(mode, func(t *testing.T) {
			cfg := StaticConfig{ETag: func(os.FileInfo) string { return etag }, Immutable: strings.HasPrefix(mode, "immutable")}
			if mode == "cold" || mode == "legacy" {
				cfg.TTL = -1
			}
			if strings.HasSuffix(mode, "stream") {
				cfg.MaxEntryBytes = 8
			}
			next := func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) }
			var h fasthttp.RequestHandler
			if mode == "legacy" {
				h = StaticWithConfig(root, cfg)(next)
			} else {
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
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					c := requestContext(t, "/asset.txt", nil)
					if tc.span != "" {
						c.Request.Header.Set("Range", tc.span)
					}
					if tc.condition != "" {
						c.Request.Header.Set("If-Range", tc.condition)
					}
					for _, pair := range tc.extra {
						c.Request.Header.Add(pair[0], pair[1])
					}
					h(c)
					if got := string(c.Response.Body()); c.Response.StatusCode() != tc.status || got != tc.body {
						t.Fatalf("status=%d body=%q, want %d/%q", c.Response.StatusCode(), got, tc.status, tc.body)
					}
					if string(c.Response.Header.Peek("ETag")) != etag {
						t.Fatal("missing validator")
					}
					contentRange := string(c.Response.Header.Peek("Content-Range"))
					if tc.status == 206 && contentRange == "" {
						t.Fatal("missing partial metadata")
					}
					if tc.status == 416 && contentRange != "bytes */36" {
						t.Fatal(contentRange)
					}
					if tc.status != 206 && tc.status != 416 && contentRange != "" {
						t.Fatal("stale Content-Range")
					}
				})
			}
			head := requestContext(t, "/asset.txt", map[string]string{"Range": "bytes=999-", "If-Range": "\"miss\""})
			head.Request.Header.SetMethod("HEAD")
			h(head)
			if head.Response.StatusCode() != 200 || head.Response.Header.ContentLength() != len(body) || len(head.Response.Body()) != 0 || head.Response.IsBodyStream() {
				t.Fatal("HEAD incorrectly applied Range")
			}
		})
	}
}

func TestStaticRangeOptionsWeakValidatorsAndOwnership(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "asset.txt")
	os.WriteFile(path, []byte("0123456789"), 0600)
	s, err := NewStaticHandler(root, StaticConfig{ETag: WeakStaticETag, MaxEntryBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Middleware(func(*fasthttp.RequestCtx) {})
	c := requestContext(t, "/asset.txt", map[string]string{"Range": "bytes=1-3"})
	h(c)
	tag := string(c.Response.Header.Peek("ETag"))
	if !strings.HasPrefix(tag, "W/") {
		t.Fatal(tag)
	}
	stream, ok := c.Response.BodyStream().(*staticLimitedFile)
	if !ok {
		t.Fatal("range not streamed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if string(c.Response.Body()) != "123" {
		t.Fatal("root close interrupted owned stream")
	}
	if _, err := stream.file.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("stream descriptor after Body: %v", err)
	}
	s, err = NewStaticHandler(root, StaticConfig{ETag: WeakStaticETag})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h = s.Middleware(func(*fasthttp.RequestCtx) {})
	for _, field := range []string{"If-None-Match", "If-Match", "If-Range"} {
		c := requestContext(t, "/asset.txt", map[string]string{field: tag, "Range": "bytes=1-3"})
		h(c)
		want := map[string]int{"If-None-Match": 304, "If-Match": 412, "If-Range": 200}[field]
		if c.Response.StatusCode() != want {
			t.Fatalf("%s status=%d", field, c.Response.StatusCode())
		}
	}
	disabled, err := NewStaticHandler(root, StaticConfig{DisableRange: true, ETag: func(os.FileInfo) string { return "\"bad\"\r\nInjected: yes" }})
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	c = requestContext(t, "/asset.txt", map[string]string{"Range": "bytes=1-3"})
	disabled.Middleware(func(*fasthttp.RequestCtx) {})(c)
	if c.Response.StatusCode() != 200 || len(c.Response.Header.Peek("Accept-Ranges")) != 0 || len(c.Response.Header.Peek("ETag")) != 0 {
		t.Fatal("invalid options handling")
	}
	if err := os.WriteFile(filepath.Join(root, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	c = requestContext(t, "/empty", map[string]string{"Range": "bytes=0-"})
	h(c)
	if c.Response.StatusCode() != 416 || string(c.Response.Header.Peek("Content-Range")) != "bytes */0" {
		t.Fatal("invalid empty-file range")
	}
}

func TestStaticRangeTCPFramingAndCompression(t *testing.T) {
	root := t.TempDir()
	body := bytes.Repeat([]byte("0123456789abcdefghijklmnopqrstuvwxyz"), 32000)
	os.WriteFile(filepath.Join(root, "asset.txt"), body, 0600)
	s, err := NewStaticHandler(root, StaticConfig{Immutable: true, MaxEntryBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &fasthttp.Server{Handler: Compress()(s.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) }))}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	defer func() { ln.Close(); server.Shutdown(); <-done }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	for _, span := range [][2]int{{7891, 262143}, {2, 9}} {
		fmt.Fprintf(conn, "GET /asset.txt HTTP/1.1\r\nHost: test\r\nAccept-Encoding: gzip\r\nRange: bytes=%d-%d\r\n\r\n", span[0], span[1])
		resp, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
		if err != nil {
			t.Fatal("response framing:", err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 206 || resp.ContentLength != int64(span[1]-span[0]+1) || !bytes.Equal(data, body[span[0]:span[1]+1]) || resp.Header.Get("Content-Encoding") != "" {
			t.Fatal("incorrect partial response")
		}
	}
}

func FuzzStaticRangeAndETag(f *testing.F) {
	for _, value := range []string{"bytes=0-9", "bytes=-8", "bytes=999999999999999999999999-", "bytes=1-2,5-6", "invalid"} {
		f.Add(value, "\"tag\"")
	}
	f.Fuzz(func(t *testing.T, value, condition string) {
		if len(value) > 65536 || len(condition) > 65536 {
			t.Skip()
		}
		var c fasthttp.RequestCtx
		c.Request.Header.Set("Range", value)
		c.Request.Header.Set("If-Range", condition)
		selected, ranged := selectStaticRange(&c, StaticConfig{}, time.Unix(100, 0), 4096, "\"tag\"")
		if ranged && selected.length > 0 && (selected.start < 0 || selected.length > 4096 || selected.start > 4096-selected.length) {
			t.Fatalf("invalid range: %+v", selected)
		}
		tag, rest := scanStaticETag(condition)
		if tag != "" && !strings.HasSuffix(tag, "\"") {
			t.Fatal("invalid parsed tag")
		}
		if tag != "" && len(rest) >= len(condition) {
			t.Fatal("scanner did not advance")
		}
	})
}
