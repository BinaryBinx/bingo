package middleware

import (
	"bufio"
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

func absoluteCacheRequest(t *testing.T, target string) *fasthttp.RequestCtx {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	if err := ctx.Request.Read(bufio.NewReader(strings.NewReader("GET " + target + " HTTP/1.1\r\nHost: example.test\r\n\r\n"))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctx.Response.Reset)
	return ctx
}

func TestCacheInvalidationNormalizesAbsoluteRequestTargets(t *testing.T) {
	for _, mode := range []string{"explicit", "write", "location"} {
		t.Run(mode, func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			read := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("old") })
			read(absoluteCacheRequest(t, "http://example.test/items?id=1"))
			read(absoluteCacheRequest(t, "http://example.test/items?id=2"))
			read(absoluteCacheRequest(t, "http://example.test/%69tems?id=1"))
			read(absoluteCacheRequest(t, "http://example.test/items?id=%31"))
			read(absoluteCacheRequest(t, "http://example.test/folder/../items?id=1"))
			switch mode {
			case "explicit":
				if err := cache.Invalidate("example.test", "/items?id=1"); err != nil {
					t.Fatal(err)
				}
			case "write", "location":
				write := cache.Middleware(func(c *fasthttp.RequestCtx) {
					c.SetStatusCode(fasthttp.StatusNoContent)
					if mode == "location" {
						c.Response.Header.Set("Location", "/items?id=1")
					}
				})
				target := "http://example.test/items?id=1"
				if mode == "location" {
					target = "http://example.test/action"
				}
				ctx := absoluteCacheRequest(t, target)
				ctx.Request.Header.SetMethod("PUT")
				write(ctx)
			}
			fresh := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("new") })
			for _, target := range []string{"/items?id=1", "http://example.test/items?id=1"} {
				ctx := absoluteCacheRequest(t, target)
				fresh(ctx)
				if string(ctx.Response.Body()) != "new" {
					t.Errorf("stale absolute-form target %q", target)
				}
			}
			for _, target := range []string{"/items?id=2", "/%69tems?id=1", "/items?id=%31", "/folder/../items?id=1"} {
				ctx := requestContext(t, target, map[string]string{"Host": "example.test"})
				fresh(ctx)
				if string(ctx.Response.Body()) != "old" {
					t.Errorf("query or escape representation merged: %q", target)
				}
			}
		})
	}
}

func TestCacheAbsoluteTargetsPreserveEmptyQuery(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	read := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("old") })
	read(absoluteCacheRequest(t, "http://example.test/items"))
	read(absoluteCacheRequest(t, "http://example.test/items?"))
	if err := cache.Invalidate("example.test", "/items"); err != nil {
		t.Fatal(err)
	}
	fresh := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("new") })
	for _, item := range []struct{ target, body string }{{"/items", "new"}, {"/items?", "old"}} {
		response := requestContext(t, item.target, map[string]string{"Host": "example.test"})
		fresh(response)
		if string(response.Response.Body()) != item.body {
			t.Errorf("target=%q body=%q, want %q", item.target, response.Response.Body(), item.body)
		}
	}
}

type cacheWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *cacheWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestCacheSuccessfulWritesInvalidateEveryVariant(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "CUSTOM"} {
		t.Run(method, func(t *testing.T) {
			var revision int
			h := Cache(time.Minute)(func(c *fasthttp.RequestCtx) {
				if c.IsGet() {
					c.Response.Header.Set("Vary", "Accept-Language")
					c.SetBodyString(fmt.Sprint(revision))
					return
				}
				revision++
				c.SetStatusCode(fasthttp.StatusNoContent)
			})
			variants := []map[string]string{
				{"Accept-Language": "en"}, {"Accept-Language": "zh"},
				{"Accept-Language": "en", "Accept-Encoding": "gzip"},
				{"Accept-Language": "en", "Origin": "https://browser.test"},
			}
			for _, headers := range variants {
				headers["Host"] = "example.test"
				h(requestContext(t, "/item?id=1", headers))
			}
			// An authenticated write must invalidate anonymous public snapshots.
			write := requestContext(t, "/item?id=1", map[string]string{"Host": "EXAMPLE.TEST", "Authorization": "Bearer editor"})
			write.Request.Header.SetMethod(method)
			h(write)
			for _, headers := range variants {
				read := requestContext(t, "/item?id=1", headers)
				h(read)
				if string(read.Response.Body()) != "1" {
					t.Fatalf("stale %s variant: %v", method, headers)
				}
			}
		})
	}
}

func TestCacheWriteInvalidationPreservesSafeAndFailedRequests(t *testing.T) {
	for _, scenario := range []struct {
		method      string
		status      int
		invalidates bool
	}{
		{"GET", 200, false}, {"HEAD", 200, false}, {"OPTIONS", 204, false}, {"TRACE", 200, false},
		{"PUT", 400, false}, {"DELETE", 500, false}, {"POST", 303, true},
	} {
		t.Run(fmt.Sprintf("%s-%d", scenario.method, scenario.status), func(t *testing.T) {
			cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
			read := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("old") })
			read(requestContext(t, "/item", map[string]string{"Host": "example.test"}))
			write := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(scenario.status) })
			request := requestContext(t, "/item", map[string]string{"Host": "example.test"})
			request.Request.Header.SetMethod(scenario.method)
			write(request)
			fresh := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("new") })
			response := requestContext(t, "/item", map[string]string{"Host": "example.test"})
			fresh(response)
			want := "old"
			if scenario.invalidates {
				want = "new"
			}
			if string(response.Response.Body()) != want {
				t.Fatalf("body=%q, want %q", response.Response.Body(), want)
			}
		})
	}
}

func TestCacheInvalidationReleasesWaitersAndFencesOldOrigins(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute, CoalesceHeaders: []string{}})
	entered, release, oldDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); <-oldDone }()
	var calls atomic.Int32
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			c.SetBodyString("old")
			return
		}
		c.SetBodyString("new")
	})
	old := requestContext(t, "/item", map[string]string{"Host": "example.test"})
	go func() { h(old); close(oldDone) }()
	<-entered
	// Done is evaluated only after joining the flight, giving a deterministic
	// barrier for a real request waiting behind the blocked origin.
	waiting := make(chan struct{})
	waiter := requestContext(t, "/item", map[string]string{"Host": "example.test"})
	requestcontext.Set(waiter, &cacheWaitContext{Context: context.Background(), waiting: waiting})
	waiterDone := make(chan struct{})
	go func() { h(waiter); close(waiterDone) }()
	defer func() { once.Do(func() { close(release) }); <-waiterDone }()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("request did not join flight")
	}
	if err := cache.Invalidate("EXAMPLE.TEST", "/item"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiterDone:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not wake waiter")
	}
	if string(waiter.Response.Body()) != "new" {
		t.Fatal("waiter reused old response")
	}
	newDone := make(chan struct{})
	fresh := requestContext(t, "/item", map[string]string{"Host": "example.test"})
	go func() { h(fresh); close(newDone) }()
	select {
	case <-newDone:
	case <-time.After(time.Second):
		t.Fatal("new request joined obsolete flight")
	}
	if string(fresh.Response.Body()) != "new" {
		t.Fatal("new request saw stale response")
	}
	once.Do(func() { close(release) })
	<-oldDone
	if string(old.Response.Body()) != "old" {
		t.Fatal("existing origin response changed")
	}
	final := requestContext(t, "/item", map[string]string{"Host": "example.test"})
	h(final)
	if calls.Load() != 2 || string(final.Response.Body()) != "new" || string(final.Response.Header.Peek("X-Cache")) != "HIT" {
		t.Fatalf("old origin repopulated cache: calls=%d body=%q cache=%q", calls.Load(), final.Response.Body(), final.Response.Header.Peek("X-Cache"))
	}
	cache.flights.mu.Lock()
	defer cache.flights.mu.Unlock()
	if len(cache.flights.items) != 0 || cache.flights.bytes != 0 {
		t.Fatal("obsolete flight retained accounting")
	}
}

func TestCacheInvalidationLocationSameOriginAndExactTarget(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	read := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("old") })
	targets := []struct {
		host, uri   string
		invalidated bool
	}{
		{"example.test", "/write", true}, {"example.test", "/related?q=1", true},
		{"example.test", "/relative", true}, {"example.test", "/related?q=2", false},
		{"other.test", "/related?q=1", false}, {"example.test", "/cross-scheme", false},
		{"example.test:8080", "/related?q=1", false},
	}
	for _, target := range targets {
		read(requestContext(t, target.uri, map[string]string{"Host": target.host}))
	}
	write := cache.Middleware(func(c *fasthttp.RequestCtx) {
		// Request rewriting must not change which resource is invalidated.
		c.Request.SetRequestURI("/rewritten")
		c.Request.Header.SetHost("other.test")
		c.Response.Header.Add("Location", "http://EXAMPLE.TEST/related?q=1#fragment")
		c.Response.Header.Add("Content-Location", "relative")
		c.Response.Header.Add("Content-Location", "//other.test/related?q=1")
		c.Response.Header.Add("Content-Location", "https://example.test/cross-scheme")
		c.Response.Header.Add("Content-Location", "http://example.test:8080/related?q=1")
		c.SetStatusCode(fasthttp.StatusCreated)
	})
	request := requestContext(t, "/write", map[string]string{"Host": "example.test"})
	request.Request.Header.SetMethod("POST")
	write(request)
	fresh := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("new") })
	for _, target := range targets {
		response := requestContext(t, target.uri, map[string]string{"Host": target.host})
		fresh(response)
		want := "old"
		if target.invalidated {
			want = "new"
		}
		if string(response.Response.Body()) != want {
			t.Errorf("%s%s body=%q, want %q", target.host, target.uri, response.Response.Body(), want)
		}
	}
}

func TestCacheExplicitInvalidationValidationAndBoundedState(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute, MaxEntries: 8, MaxBytes: 8192})
	for _, target := range []string{"https://other.test/item", "item", "/item#fragment", "/item\r\nHost: other.test", "/%invalid"} {
		if err := cache.Invalidate("example.test", target); err == nil {
			t.Errorf("accepted invalid target %q", target)
		}
	}
	if err := cache.Invalidate("example.test/evil", "/"); err == nil {
		t.Fatal("accepted malformed Host")
	}
	h := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString(strings.Repeat("body", 16)) })
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Go(func() {
			for j := 0; j < 200; j++ {
				uri := fmt.Sprintf("/items/%d/%d", i, j)
				var request fasthttp.RequestCtx
				request.Request.SetRequestURI(uri)
				request.Request.Header.SetHost("example.test")
				h(&request)
				request.Response.Reset()
				if err := cache.Invalidate("example.test", uri); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Wait()
	cache.cache.mu.Lock()
	defer cache.cache.mu.Unlock()
	if len(cache.cache.items) != 0 || cache.cache.bytes != 0 || cache.cache.timer != nil {
		t.Fatal("invalidations retained response storage")
	}
}

func TestCacheUncacheableOldOriginCannotDeleteNewVaryIndex(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute, CoalesceHeaders: []string{}})
	release, entered, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); <-done }()
	oldHandler := cache.Middleware(func(c *fasthttp.RequestCtx) {
		close(entered)
		<-release
		c.Response.Header.Set("Cache-Control", "private")
		c.SetBodyString("old")
	})
	old := requestContext(t, "/item", map[string]string{"Host": "example.test"})
	go func() { oldHandler(old); close(done) }()
	<-entered
	if err := cache.Invalidate("example.test", "/item"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	freshHandler := cache.Middleware(func(c *fasthttp.RequestCtx) {
		calls++
		c.Response.Header.Set("Vary", "Accept-Language")
		c.SetBody(c.Request.Header.Peek("Accept-Language"))
	})
	for _, language := range []string{"en", "zh"} {
		freshHandler(requestContext(t, "/item", map[string]string{"Host": "example.test", "Accept-Language": language}))
	}
	once.Do(func() { close(release) })
	<-done
	for _, language := range []string{"en", "zh"} {
		response := requestContext(t, "/item", map[string]string{"Host": "example.test", "Accept-Language": language})
		freshHandler(response)
		if string(response.Response.Header.Peek("X-Cache")) != "HIT" || string(response.Response.Body()) != language {
			t.Fatalf("old response removed new schema: %q", response.Response.Header.Header())
		}
	}
	if calls != 2 {
		t.Fatalf("unexpected refills: %d", calls)
	}
}

func TestCacheLateSuccessfulWriteInvalidatesAfterCancellation(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	read := cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("old") })
	read(requestContext(t, "/item", map[string]string{"Host": "example.test"}))
	worker, cancel := context.WithCancel(context.Background())
	defer cancel()
	write := cache.Middleware(func(c *fasthttp.RequestCtx) {
		cancel() // A write can commit even if the client deadline expires.
		c.SetStatusCode(fasthttp.StatusNoContent)
	})
	request := requestContext(t, "/item", map[string]string{"Host": "example.test"})
	request.Request.Header.SetMethod("PUT")
	requestcontext.SetTimeout(request, worker)
	write(request)
	response := requestContext(t, "/item", map[string]string{"Host": "example.test"})
	cache.Middleware(func(c *fasthttp.RequestCtx) { c.SetBodyString("new") })(response)
	if string(response.Response.Body()) != "new" {
		t.Fatal("late committed write retained stale cache")
	}
}
