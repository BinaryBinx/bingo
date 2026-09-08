package middleware

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCacheReclaimsExpiredSnapshotsWithoutTraffic(t *testing.T) {
	c := newBoundedCache[string, []byte](10, 10<<20)
	c.put("large", make([]byte, 2<<20), 2<<20, time.Now().Add(20*time.Millisecond))
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		size, count := c.bytes, len(c.items)
		stopped := c.timer == nil
		c.mu.Unlock()
		if size == 0 && count == 0 && stopped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expired storage retained %d bytes/%d entries", size, count)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCacheBudgetAndReplacement(t *testing.T) {
	c := newBoundedCache[string, string](3, 10)
	for i := 0; i < 100; i++ {
		c.put(fmt.Sprint(i), "data", 4, time.Now().Add(time.Hour))
		c.mu.Lock()
		count, bytes := len(c.items), c.bytes
		c.mu.Unlock()
		if count > 2 || bytes > 10 {
			t.Fatalf("budget exceeded: %d/%d", count, bytes)
		}
	}
	c.put("99", "updated", 7, time.Now().Add(time.Millisecond))
	if v, ok := c.get("99", time.Now()); !ok || v != "updated" {
		t.Fatal("replacement lost")
	}
	if _, ok := c.get("99", time.Now().Add(time.Second)); ok {
		t.Fatal("expired hit")
	}
}

func TestCacheRejectsOversizedResponse(t *testing.T) {
	calls := 0
	h := CacheWithConfig(CacheConfig{Duration: time.Minute, MaxEntryBytes: 1024})(func(ctx *fasthttp.RequestCtx) { calls++; ctx.SetBodyString(strings.Repeat("x", 1024)) })
	h(newCacheGetRequest("/", "", "", ""))
	h(newCacheGetRequest("/", "", "", ""))
	if calls != 2 {
		t.Fatal("snapshot over entry budget was cached")
	}
}

func TestCacheFreshnessAndConditionalRequests(t *testing.T) {
	for _, cc := range []string{"max-age=0", "max-age=-1", "max-age=9223372036854775807", "max-age=10, max-age=20"} {
		t.Run(cc, func(t *testing.T) {
			calls := 0
			h := Cache(time.Hour)(func(ctx *fasthttp.RequestCtx) {
				calls++
				ctx.Response.Header.Set("Cache-Control", cc)
				ctx.SetBodyString("ok")
			})
			h(newCacheGetRequest("/", "", "", ""))
			h(newCacheGetRequest("/", "", "", ""))
			if calls != 2 {
				t.Fatal("invalid/stale freshness cached")
			}
		})
	}
	calls := 0
	h := Cache(time.Hour)(func(ctx *fasthttp.RequestCtx) {
		calls++
		ctx.Response.Header.Set("Cache-Control", "max-age=0, s-maxage=100")
		ctx.Response.Header.Set("Age", "10")
		ctx.SetBodyString("ok")
	})
	h(newCacheGetRequest("/", "", "", ""))
	hit := newCacheGetRequest("/", "", "", "")
	h(hit)
	if calls != 1 || string(hit.Response.Header.Peek("Age")) != "10" {
		t.Fatal("shared freshness precedence or age lost")
	}
	for _, header := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since"} {
		ctx := newCacheGetRequest("/", "", "", "")
		ctx.Request.Header.Set(header, "value")
		h(ctx)
		if string(ctx.Response.Header.Peek("X-Cache")) == "HIT" {
			t.Fatalf("conditional request %s bypassed handler", header)
		}
	}
}

func TestCacheKeyUsesAllEncodingLines(t *testing.T) {
	h := Cache(time.Minute)(func(ctx *fasthttp.RequestCtx) {
		ctx.SetBodyString(joinHeaderValues(ctx.Request.Header.PeekAll("Accept-Encoding")))
		ctx.Response.Header.Set("Vary", "Accept-Encoding")
	})
	a := newCacheGetRequest("/", "", "", "gzip")
	a.Request.Header.Add("Accept-Encoding", "br")
	b := newCacheGetRequest("/", "", "", "gzip")
	b.Request.Header.Add("Accept-Encoding", "identity")
	h(a)
	h(b)
	if string(a.Response.Body()) == string(b.Response.Body()) {
		t.Fatal("second encoding field ignored")
	}
}

func TestStaticStreamsGrowingFileWhole(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "growing")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString("small")
	oldInfo, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	payload := "small" + strings.Repeat("more", 1024)
	_, _ = f.WriteString(payload[5:])
	_, _ = f.Seek(0, io.SeekStart)
	ctx := newCacheGetRequest("/", "", "", "")
	_, streamed, err := staticBody(ctx, f, oldInfo, 64)
	if err != nil || !streamed {
		t.Fatalf("expected stream, got %v/%v", streamed, err)
	}
	if got := string(ctx.Response.Body()); got != payload {
		t.Fatalf("growing file truncated: %d != %d", len(got), len(payload))
	}
}
