package middleware

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestStaticBodyPresizingHandlesMutableFiles(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		limit               int64
		stream              bool
	}{
		{"unchanged", "original", "original", 16, false},
		{"empty", "", "", 16, false},
		{"shrink", "original", "small", 16, false},
		{"truncate", "original", "", 16, false},
		{"grow-one-byte", "old", "more", 16, false},
		{"grow-from-empty", "", "new", 16, false},
		{"at-budget", strings.Repeat("x", 16), strings.Repeat("x", 16), 16, false},
		{"grow-to-budget", "x", strings.Repeat("y", 16), 16, false},
		{"grow-past-budget", "x", strings.Repeat("y", 32), 16, true},
		{"sentinel-past-budget", strings.Repeat("x", 16), strings.Repeat("y", 17), 16, true},
		{"already-large", strings.Repeat("x", 32), strings.Repeat("x", 32), 16, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "asset")
			if err := os.WriteFile(path, []byte(tc.before), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.after), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var c fasthttp.RequestCtx
			defer c.Response.Reset()
			data, streamed, err := staticBody(&c, f, info, tc.limit)
			if err != nil || streamed != tc.stream {
				t.Fatalf("stream=%t want %t error=%v", streamed, tc.stream, err)
			}
			if streamed {
				if _, err := f.Stat(); err != nil {
					t.Fatalf("stream file closed prematurely: %v", err)
				}
				data = c.Response.Body()
			}
			if string(data) != tc.after {
				t.Fatalf("size change lost bytes: %q want %q", data, tc.after)
			}
			var sentinel [1]byte
			if _, err := f.Read(sentinel[:]); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("completed response did not close the file: %v", err)
			}
		})
	}
}

type staticBodySizedInfo struct {
	os.FileInfo
	size int64
}

func (i staticBodySizedInfo) Size() int64 { return i.size }

func TestStaticBodyBoundsPresizingAndPropagatesReadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	const largest = int64(1<<63 - 1)
	cfg := normalizeStaticConfig(StaticConfig{MaxBytes: largest, MaxEntryBytes: largest})
	if cfg.MaxEntryBytes != int64(^uint(0)>>1)-1 {
		t.Fatal("snapshot plus sentinel can overflow a slice length")
	}
	for _, size := range []int64{-1, int64(^uint(0) >> 1), largest} {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var c fasthttp.RequestCtx
		_, streamed, err := staticBody(&c, f, staticBodySizedInfo{info, size}, largest)
		if err != nil || !streamed {
			f.Close()
			t.Fatalf("unsafe size %d was not streamed: %v", size, err)
		}
		if string(c.Response.Body()) != "data" {
			t.Error("size fallback lost file contents")
		}
		c.Response.Reset()
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var c fasthttp.RequestCtx
	defer c.Response.Reset()
	data, streamed, err := staticBody(&c, f, info, 16)
	if !errors.Is(err, os.ErrClosed) || streamed || data != nil || c.Response.IsBodyStream() {
		t.Fatalf("read error became a valid body: data=%q stream=%t err=%v", data, streamed, err)
	}
}
