//go:build linux

package middleware

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestStaticETagPanicClosesOpenedDescriptor(t *testing.T) {
	countFDs := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip("descriptor inspection unavailable:", err)
		}
		return len(entries)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "asset.txt"), []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s, err := NewStaticHandler(root, StaticConfig{ETag: func(os.FileInfo) string {
		calls++
		if calls%2 == 0 {
			panic("metadata callback after Open")
		}
		return "\"asset\""
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Middleware(func(*fasthttp.RequestCtx) { t.Fatal("static route missed") })
	previous := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(previous) // finalizers must not mask missing Close calls
	before := countFDs()
	panics := 0
	for range 16 {
		func() {
			defer func() {
				if recover() != nil {
					panics++
				}
			}()
			var c fasthttp.RequestCtx
			defer c.Response.Reset()
			c.Request.SetRequestURI("/asset.txt")
			h(&c)
		}()
	}
	if panics != 16 {
		t.Fatal("callback did not exercise opened-file path", panics)
	}
	if after := countFDs(); after > before {
		t.Fatalf("leaked descriptors: before=%d after=%d", before, after)
	}
}
