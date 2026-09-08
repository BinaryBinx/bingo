package middleware

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
)

func TestCacheCoalescesConcurrentColdMisses(t *testing.T) {
	for _, cacheable := range []bool{true, false} {
		t.Run(fmt.Sprint(cacheable), func(t *testing.T) {
			const count = 64
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			h := Cache(time.Minute)(func(c *fasthttp.RequestCtx) {
				if calls.Add(1) == 1 {
					close(entered)
				}
				<-release
				if !cacheable {
					c.Response.Header.Set("Cache-Control", "private")
				}
				c.Response.Header.Set("Vary", "Accept-Language")
				c.SetBodyString("public English response")
			})
			var workers sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < count; i++ {
				workers.Go(func() {
					<-start
					var c fasthttp.RequestCtx
					c.Request.SetRequestURI("/")
					c.Request.Header.Set("Accept-Language", "en")
					h(&c)
					if string(c.Response.Body()) != "public English response" {
						t.Error("wrong coalesced response")
					}
					c.Response.Reset()
				})
			}
			close(start)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("leader did not start")
			}
			// Keep the origin busy while the concurrent request burst arrives.
			time.Sleep(30 * time.Millisecond)
			releaseOnce.Do(func() { close(release) })
			workers.Wait()
			wanted := int32(1)
			if !cacheable {
				wanted = count
			}
			if calls.Load() != wanted {
				t.Fatalf("backend calls=%d, want %d", calls.Load(), wanted)
			}
		})
	}
}

func TestCacheColdVaryAndWaiterCancellationIsolation(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	h := Cache(time.Minute)(func(c *fasthttp.RequestCtx) {
		language := string(c.Request.Header.Peek("Accept-Language"))
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		c.Response.Header.Set("Vary", "Accept-Language")
		c.SetBodyString(language)
	})
	request := func(language string) *fasthttp.RequestCtx {
		c := &fasthttp.RequestCtx{}
		c.Request.SetRequestURI("/")
		c.Request.Header.Set("Accept-Language", language)
		return c
	}
	leader := request("en")
	done := make(chan struct{})
	go func() { h(leader); close(done) }()
	<-entered
	// A different, not-yet-known Vary dimension must execute independently.
	chinese := request("zh")
	chineseDone := make(chan struct{})
	go func() { h(chinese); close(chineseDone) }()
	select {
	case <-chineseDone:
	case <-time.After(time.Second):
		t.Fatal("cold Vary request incorrectly joined English leader")
	}
	if string(chinese.Response.Body()) != "zh" {
		t.Fatal("mixed Vary representations")
	}
	waiter := request("en")
	waitContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	requestcontext.Set(waiter, waitContext)
	waitDone := make(chan struct{})
	go func() { h(waiter); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("waiter ignored its deadline")
	}
	if waiter.Response.StatusCode() != 408 || calls.Load() != 2 {
		t.Fatal("waiter executed backend or changed leader")
	}
	releaseOnce.Do(func() { close(release) })
	<-done
	for _, language := range []string{"en", "zh"} {
		c := request(language)
		h(c)
		if string(c.Response.Body()) != language || string(c.Response.Header.Peek("X-Cache")) != "HIT" {
			t.Fatal("leader result was lost or crossed variants")
		}
	}
}

func TestCachePanicReleasesWaiters(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	h := Recovery()(Cache(time.Minute)(func(c *fasthttp.RequestCtx) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			panic("origin failure")
		}
		c.SetBodyString("retry")
	}))
	first, second := &fasthttp.RequestCtx{}, &fasthttp.RequestCtx{}
	first.Request.SetRequestURI("/")
	second.Request.SetRequestURI("/")
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() { h(first); close(firstDone) }()
	<-entered
	go func() { h(second); close(secondDone) }()
	close(release)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("panic stranded waiter")
	}
	<-firstDone
	if first.Response.StatusCode() != 500 || string(second.Response.Body()) != "retry" {
		t.Fatal("panic response was shared")
	}
}

func TestResponseFlightsBoundMemoryAndDistinguishHeaders(t *testing.T) {
	g := &responseFlights{maxEntries: 2, maxBytes: 2048}
	var h fasthttp.RequestHeader
	h.SetRequestURI("/")
	first, leader := g.join(responseCacheKey{}, &h)
	if first == nil || !leader {
		t.Fatal("first admission failed")
	}
	joined, leader := g.join(responseCacheKey{}, &h)
	if joined != first || leader {
		t.Fatal("identical request did not join")
	}
	h.Add("X-Variant", "")
	second, leader := g.join(responseCacheKey{}, &h)
	if second == nil || second == first || !leader {
		t.Fatal("missing and empty headers conflated")
	}
	h.Add("X-Variant", "second")
	if extra, _ := g.join(responseCacheKey{}, &h); extra != nil {
		t.Fatal("in-flight entry limit exceeded")
	}
	g.finish(first)
	g.finish(second)
	if len(g.items) != 0 || g.bytes != 0 {
		t.Fatal("completed flight storage retained")
	}
	h.Set("X-Large", strings.Repeat("x", 4096))
	if oversized, _ := g.join(responseCacheKey{}, &h); oversized != nil {
		t.Fatal("oversized in-flight key admitted")
	}
}

func TestShardedCacheGlobalBoundsAndConcurrentReplacement(t *testing.T) {
	c := newBoundedCache[string, string](7, 40)
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Go(func() {
			for i := 0; i < 300; i++ {
				key := fmt.Sprint((worker*7 + i) % 31)
				c.put(key, key, 8, time.Now().Add(time.Minute))
				if value, ok := c.get(key, time.Now()); ok && value != key {
					t.Error("cross-key snapshot")
				}
				c.mu.Lock()
				if len(c.items) > 5 || c.bytes > 40 {
					t.Error("global capacity split or exceeded")
				}
				c.mu.Unlock()
			}
		})
	}
	workers.Wait()
	for i := 0; i < 31; i++ {
		c.delete(fmt.Sprint(i))
	}
	for i := 0; i < 31; i++ {
		if _, ok := c.get(fmt.Sprint(i), time.Now()); ok {
			t.Fatal("shard retained deleted snapshot")
		}
	}
}

func TestCompressOriginalNegotiationAndCheapSkip(t *testing.T) {
	for _, accept := range []string{"gzip", "gzip;q=0", "gzip;", "gzip;q=1;", "gzip;q=NaN"} {
		h := Compress()(func(c *fasthttp.RequestCtx) {
			c.Request.Header.Set("Accept-Encoding", "gzip")
			c.SetBodyString(strings.Repeat("payload", 100))
		})
		c := &fasthttp.RequestCtx{}
		c.Request.Header.Set("Accept-Encoding", accept)
		h(c)
		if got := string(c.Response.Header.Peek("Content-Encoding")) == "gzip"; got != (accept == "gzip") {
			t.Fatalf("changed original negotiation %q", accept)
		}
	}
	h := Compress()(func(c *fasthttp.RequestCtx) {
		c.Response.Header.Set("Vary", "Origin")
		c.Response.Header.Add("Cache-Control", "public")
		c.Response.Header.Add("Cache-Control", "No-Transform")
		c.SetBodyString(strings.Repeat("payload", 100))
	})
	c := &fasthttp.RequestCtx{}
	c.Request.Header.Set("Accept-Encoding", "gzip")
	h(c)
	if len(c.Response.Header.Peek("Content-Encoding")) != 0 || !strings.Contains(string(c.Response.Header.Peek("Vary")), "Origin") {
		t.Fatal("no-transform or Vary ignored")
	}
}
