package middleware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func writeStaticFixture(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "asset.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedStaticReloadAndClose(t *testing.T) {
	for _, immutable := range []bool{false, true} {
		t.Run(fmt.Sprint(immutable), func(t *testing.T) {
			root, replacement := t.TempDir(), t.TempDir()
			writeStaticFixture(t, root, "old")
			writeStaticFixture(t, replacement, "replacement")
			s, err := NewStaticHandler(root, StaticConfig{Immutable: immutable})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			h := s.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) })
			get := func(want string) *fasthttp.RequestCtx {
				c := requestContext(t, "/asset.txt", nil)
				h(c)
				if c.Response.StatusCode() != 200 || string(c.Response.Body()) != want {
					t.Fatalf("body=%q status=%d, want %q", c.Response.Body(), c.Response.StatusCode(), want)
				}
				return c
			}
			get("old")
			writeStaticFixture(t, root, "changed content")
			want := "changed content"
			if immutable {
				want = "old"
			}
			current := get(want)
			head := requestContext(t, "/asset.txt", nil)
			head.Request.Header.SetMethod("HEAD")
			h(head)
			if len(head.Response.Body()) != 0 || head.Response.Header.ContentLength() != len(want) {
				t.Fatal("snapshot HEAD mismatch")
			}
			conditional := requestContext(t, "/asset.txt", map[string]string{"If-Modified-Since": string(current.Response.Header.Peek("Last-Modified"))})
			h(conditional)
			if conditional.Response.StatusCode() != 304 || len(conditional.Response.Body()) != 0 {
				t.Fatal("snapshot conditional response mismatch")
			}
			oldRoot, oldCache := s.root, s.cache
			if err := s.Reload(""); err != nil {
				t.Fatal(err)
			}
			get("changed content")
			if _, err := oldRoot.Stat("asset.txt"); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("reload retained root: %v", err)
			}
			oldCache.mu.Lock()
			retained := len(oldCache.items) != 0 || oldCache.bytes != 0 || oldCache.timer != nil
			oldCache.mu.Unlock()
			if retained {
				t.Fatal("reload retained cache or timer")
			}
			if err := s.Reload(filepath.Join(root, "missing")); err == nil {
				t.Fatal("invalid reload accepted")
			}
			get("changed content")
			if err := s.Reload(replacement); err != nil {
				t.Fatal(err)
			}
			get("replacement")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close not idempotent: %v", err)
			}
			closed := requestContext(t, "/asset.txt", nil)
			h(closed)
			if closed.Response.StatusCode() != 503 {
				t.Fatal("closed handler served new request")
			}
			if err := s.Reload(root); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("reload revived closed handler: %v", err)
			}
		})
	}
}

func TestManagedStaticImmutableExpiryAndDisabledCache(t *testing.T) {
	for _, ttl := range []time.Duration{time.Hour, -1} {
		root := t.TempDir()
		writeStaticFixture(t, root, "original")
		s, err := NewStaticHandler(root, StaticConfig{Immutable: true, TTL: ttl})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		h := s.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) })
		first := requestContext(t, "/asset.txt", nil)
		h(first)
		writeStaticFixture(t, root, "new")
		if ttl > 0 {
			// Expire the immutable entry under the store's normal publication path.
			item, ok := s.cache.get("asset.txt", time.Now())
			if !ok {
				t.Fatal("snapshot not retained")
			}
			s.cache.put("asset.txt", item, 512, time.Now().Add(time.Millisecond))
			time.Sleep(5 * time.Millisecond)
		}
		second := requestContext(t, "/asset.txt", nil)
		h(second)
		if string(second.Response.Body()) != "new" {
			t.Fatal("expired/disabled immutable snapshot served stale data")
		}
	}
}

func TestManagedStaticStreamsSurviveReloadAndClose(t *testing.T) {
	root, replacement := t.TempDir(), t.TempDir()
	body := strings.Repeat("stream", 100)
	writeStaticFixture(t, root, body)
	writeStaticFixture(t, replacement, "new stream")
	s, err := NewStaticHandler(root, StaticConfig{MaxEntryBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) })
	first := requestContext(t, "/asset.txt", nil)
	h(first)
	if !first.Response.IsBodyStream() {
		t.Fatal("large file buffered")
	}
	if err := s.Reload(replacement); err != nil {
		t.Fatal(err)
	}
	second := requestContext(t, "/asset.txt", nil)
	h(second)
	if !second.Response.IsBodyStream() {
		t.Fatal("replacement file buffered")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if string(first.Response.Body()) != body || string(second.Response.Body()) != "new stream" {
		t.Fatal("lifecycle closed an active response stream")
	}
}

func TestManagedStaticConcurrentLifecycleAndFallthrough(t *testing.T) {
	root, replacement := t.TempDir(), t.TempDir()
	writeStaticFixture(t, root, "first")
	writeStaticFixture(t, replacement, "second")
	s, err := NewStaticHandler(root, StaticConfig{Immutable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(404) })
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Go(func() {
			for j := 0; j < 100; j++ {
				var c fasthttp.RequestCtx
				c.Request.SetRequestURI("/asset.txt")
				h(&c)
				if status := c.Response.StatusCode(); status != 503 && (status != 200 || (string(c.Response.Body()) != "first" && string(c.Response.Body()) != "second")) {
					t.Error("mixed or invalid generation")
				}
				c.Response.Reset()
			}
		})
	}
	for i := 0; i < 20; i++ {
		if err := s.Reload([]string{root, replacement}[i%2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	for _, method := range []string{"GET", "POST"} {
		s, err := NewStaticHandler(root, StaticConfig{})
		if err != nil {
			t.Fatal(err)
		}
		c := requestContext(t, "/missing", nil)
		c.Request.Header.SetMethod(method)
		done := make(chan struct{})
		go func() { s.Middleware(func(*fasthttp.RequestCtx) { s.Close() })(c); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("downstream Close deadlocked")
		}
	}
}
