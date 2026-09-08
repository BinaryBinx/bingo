package middleware

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func TestCacheReplacementKeepsWarmVaryVariantReadable(t *testing.T) {
	cache := NewCacheHandler(CacheConfig{Duration: time.Minute})
	defer cache.Close()
	var calls atomic.Int32
	h := cache.Middleware(func(c *fasthttp.RequestCtx) {
		calls.Add(1)
		c.Response.Header.Set("Vary", "X-Variant")
		c.SetBody(c.Request.Header.Peek("X-Variant"))
	})
	request := func(value string) string {
		var c fasthttp.RequestCtx
		defer c.Response.Reset()
		c.Request.SetRequestURI("http://example.test/vary")
		c.Request.Header.Set("X-Variant", value)
		h(&c)
		if string(c.Response.Body()) != value {
			t.Error("wrong variant body")
		}
		return string(c.Response.Header.Peek("X-Cache"))
	}
	request("hot")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var armed atomic.Bool
	armed.Store(true)
	groupOf := cache.cache.groupOf
	cache.cache.groupOf = func(key responseCacheKey) cacheGroupKey {
		if key.variant == "" && armed.CompareAndSwap(true, false) {
			// Pause index replacement while its writer owns the global lock.
			// Readers must still see the old unexpired index without waiting.
			close(entered)
			<-release
		}
		return groupOf(key)
	}
	coldDone, hotDone := make(chan struct{}), make(chan string, 1)
	defer func() { once.Do(func() { close(release) }); <-coldDone }()
	go func() { defer close(coldDone); request("cold") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("replacement did not start")
	}
	go func() { hotDone <- request("hot") }()
	select {
	case status := <-hotDone:
		if status != "HIT" {
			t.Error("warm variant missed during replacement")
		}
	case <-time.After(time.Second):
		once.Do(func() { close(release) })
		<-hotDone
		t.Error("warm reader blocked or reached origin during replacement")
	}
	once.Do(func() { close(release) })
	<-coldDone
	if calls.Load() != 2 || request("cold") != "HIT" || request("hot") != "HIT" {
		t.Fatalf("replacement caused unnecessary origin requests: %d", calls.Load())
	}
}

func TestCacheReplacementPreservesBudgetExpiryAndGroupDeletion(t *testing.T) {
	cache := newBoundedCache[string, string](3, 6)
	defer cache.clear()
	group := cacheGroupKey{"example.test", "/item"}
	cache.groupOf = func(string) cacheGroupKey { return group }
	oldExpiry := time.Now().Add(time.Minute)
	for _, key := range []string{"target", "other", "another"} {
		cache.put(key, key, 2, oldExpiry)
	}
	cache.put("target", "new", 5, oldExpiry.Add(time.Hour))
	if value, ok := cache.get("target", oldExpiry.Add(time.Second)); !ok || value != "new" {
		t.Fatal("replacement used the old value or expiry")
	}
	cache.mu.Lock()
	entries, bytes, expiry, groups := len(cache.items), cache.bytes, len(cache.expiry), len(cache.groups)
	cache.mu.Unlock()
	if entries != 1 || bytes != 5 || expiry != 1 || groups != 1 {
		t.Fatalf("replacement bookkeeping: entries=%d bytes=%d expiry=%d groups=%d", entries, bytes, expiry, groups)
	}
	cache.put("target", "oversized", 7, oldExpiry)
	cache.put("target", "expired", 1, time.Now().Add(-time.Second))
	if value, ok := cache.get("target", time.Now()); !ok || value != "new" {
		t.Fatal("rejected replacement lost old snapshot")
	}
	cache.deleteGroup(group)
	if _, ok := cache.get("target", time.Now()); ok {
		t.Fatal("replacement escaped group invalidation")
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.items) != 0 || cache.bytes != 0 || len(cache.expiry) != 0 || len(cache.groups) != 0 || cache.timer != nil {
		t.Fatal("group invalidation retained replacement metadata")
	}
}
