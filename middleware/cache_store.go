package middleware

import (
	"container/heap"
	"sync"
	"time"
)

// boundedCache owns immutable snapshots. Expiry order also provides bounded-cost
// eviction: no request scans the entire map to find an eviction candidate.
// The one-shot expiry timer stops when empty; no permanent cleanup goroutine is used.
type boundedCache[K comparable, V any] struct {
	mu         sync.Mutex
	items      map[K]*cacheEntry[K, V]
	expiry     entryHeap[K, V]
	bytes      int64
	maxBytes   int64
	maxEntries int
	timer      *time.Timer
}

type cacheEntry[K comparable, V any] struct {
	key     K
	value   V
	cost    int64
	expires time.Time
	index   int
}

type entryHeap[K comparable, V any] []*cacheEntry[K, V]

func (h entryHeap[K, V]) Len() int           { return len(h) }
func (h entryHeap[K, V]) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }
func (h entryHeap[K, V]) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *entryHeap[K, V]) Push(x any) {
	e := x.(*cacheEntry[K, V])
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *entryHeap[K, V]) Pop() any {
	old := *h
	n := len(old) - 1
	e := old[n]
	old[n] = nil
	*h = old[:n]
	return e
}

func newBoundedCache[K comparable, V any](entries int, bytes int64) *boundedCache[K, V] {
	return &boundedCache[K, V]{items: make(map[K]*cacheEntry[K, V]), maxEntries: entries, maxBytes: bytes}
}

func (c *boundedCache[K, V]) remove(e *cacheEntry[K, V]) {
	delete(c.items, e.key)
	c.bytes -= e.cost
	heap.Remove(&c.expiry, e.index)
}

func (c *boundedCache[K, V]) get(key K, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		if now.Before(e.expires) {
			return e.value, true
		}
		c.remove(e)
	}
	var zero V
	return zero, false
}

func (c *boundedCache[K, V]) delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.items[key]; e != nil {
		c.remove(e)
		c.scheduleExpiry()
	}
}

func (c *boundedCache[K, V]) put(key K, value V, cost int64, expires time.Time) {
	if cost <= 0 || cost > c.maxBytes || !time.Now().Before(expires) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.items[key]; old != nil {
		c.remove(old)
	}
	for len(c.items) >= c.maxEntries || cost > c.maxBytes-c.bytes {
		c.remove(c.expiry[0])
	}
	e := &cacheEntry[K, V]{key: key, value: value, cost: cost, expires: expires}
	c.items[key] = e
	c.bytes += cost
	heap.Push(&c.expiry, e)
	c.scheduleExpiry()
}

func (c *boundedCache[K, V]) scheduleExpiry() {
	if c.timer != nil {
		c.timer.Stop()
	}
	if len(c.expiry) == 0 {
		c.timer = nil
		return
	}
	delay := time.Until(c.expiry[0].expires)
	if c.timer == nil {
		c.timer = time.AfterFunc(delay, c.expire)
	} else {
		c.timer.Reset(delay)
	}
}

func (c *boundedCache[K, V]) expire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for len(c.expiry) > 0 && !now.Before(c.expiry[0].expires) {
		c.remove(c.expiry[0])
	}
	c.scheduleExpiry()
}
