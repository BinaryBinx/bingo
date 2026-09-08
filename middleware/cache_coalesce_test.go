package middleware

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BinaryBinx/bingo/internal/requestcontext"
	"github.com/valyala/fasthttp"
)

func TestCacheCoalesceIgnoresTraceIDsAndPreservesPrivacy(t *testing.T) {
	for _, scenario := range []string{"public", "private", "authorization", "cookie", "signature"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			h := CacheWithConfig(CacheConfig{Duration: time.Minute, CoalesceHeaders: []string{}})(func(c *fasthttp.RequestCtx) {
				calls.Add(1)
				<-release
				if scenario == "private" {
					c.Response.Header.Set("Cache-Control", "private")
				}
				if scenario == "signature" {
					c.Response.Header.Set("Signature", "per-response-signature")
				}
				c.SetBodyString("public")
			})
			var workers sync.WaitGroup
			for i := 0; i < 64; i++ {
				workers.Go(func() {
					var c fasthttp.RequestCtx
					defer c.Response.Reset()
					c.Request.SetRequestURI("/")
					c.Request.Header.Set("X-Request-ID", fmt.Sprint(i))
					c.Request.Header.Set("Traceparent", fmt.Sprint(i))
					if scenario == "authorization" {
						c.Request.Header.Set("Authorization", "Bearer user")
					}
					if scenario == "cookie" {
						c.Request.Header.Set("Cookie", "session=user")
					}
					h(&c)
					if string(c.Response.Body()) != "public" {
						t.Error("incorrect body")
					}
				})
			}
			time.Sleep(30 * time.Millisecond)
			close(release)
			workers.Wait()
			want := int32(64)
			if scenario == "public" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("backend calls=%d, want %d", calls.Load(), want)
			}
		})
	}
}

func TestCacheConfiguredCoalesceRechecksVaryAndCancellation(t *testing.T) {
	var calls atomic.Int32
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); <-done }()
	h := CacheWithConfig(CacheConfig{Duration: time.Minute, CoalesceHeaders: []string{}})(func(c *fasthttp.RequestCtx) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		c.Response.Header.Set("Vary", "Accept-Language")
		c.SetBody(c.Request.Header.Peek("Accept-Language"))
	})
	leader := requestContext(t, "/", map[string]string{"Accept-Language": "en"})
	go func() { h(leader); close(done) }()
	<-entered
	waiter := requestContext(t, "/", map[string]string{"Accept-Language": "zh"})
	waitContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	requestcontext.Set(waiter, waitContext)
	h(waiter)
	if waiter.Response.StatusCode() != 408 || calls.Load() != 1 {
		t.Fatal("configured waiter did not join/cancel independently")
	}
	chinese := requestContext(t, "/", map[string]string{"Accept-Language": "zh"})
	chineseDone := make(chan struct{})
	go func() { h(chinese); close(chineseDone) }()
	once.Do(func() { close(release) })
	<-chineseDone
	<-done
	if string(chinese.Response.Body()) != "zh" || string(leader.Response.Body()) != "en" || calls.Load() != 2 {
		t.Fatal("cold Vary representations were conflated")
	}
	for _, language := range []string{"en", "zh"} {
		c := requestContext(t, "/", map[string]string{"Accept-Language": language})
		h(c)
		if string(c.Response.Body()) != language || string(c.Response.Header.Peek("X-Cache")) != "HIT" {
			t.Fatal("incorrect cached Vary representation")
		}
	}
}

func TestConfiguredFlightHeaderDimensions(t *testing.T) {
	input := []string{" X-Variant ", "x-variant"}
	headers := normalizeCoalesceHeaders(input)
	input[0] = "Traceparent"
	g := responseFlights{maxEntries: 8, maxBytes: 4096, headers: headers}
	var h fasthttp.RequestHeader
	first, _ := g.join(responseCacheKey{}, &h)
	h.Set("Traceparent", "unique")
	if flight, leader := g.join(responseCacheKey{}, &h); flight != first || leader {
		t.Fatal("unselected field split flight")
	}
	h.Add("X-Variant", "")
	second, leader := g.join(responseCacheKey{}, &h)
	if second == first || !leader {
		t.Fatal("empty and missing selected field conflated")
	}
	h.Add("X-Variant", "second")
	third, leader := g.join(responseCacheKey{}, &h)
	if third == second || !leader {
		t.Fatal("repeated selected field ignored")
	}
	for _, f := range []*responseFlight{first, second, third} {
		g.finish(f)
	}
	if g.bytes != 0 {
		t.Fatal("flight memory retained")
	}
	if normalizeCoalesceHeaders([]string{"invalid field"}) != nil {
		t.Fatal("invalid configuration should preserve full-header grouping")
	}
}
