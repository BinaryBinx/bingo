package middleware

import (
	"container/heap"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

const cacheShardCount = 32

// boundedCache owns immutable snapshots. Second-chance links select eviction candidates;
// an expiry heap removes stale entries without scanning the entire map.
// The one-shot expiry timer stops when empty; no permanent cleanup goroutine is used.
// Writers hold mu before taking a shard lock. Readers only take one shard lock;
// global entry/byte limits therefore remain exact without serializing every hit.
type boundedCache[K comparable, V any] struct {
	mu             sync.Mutex
	items          map[K]*cacheEntry[K, V]
	expiry         entryHeap[K, V]
	newest, oldest *cacheEntry[K, V]
	bytes          int64
	maxBytes       int64
	maxEntries     int
	timer          *time.Timer
	seed           maphash.Seed
	shards         [cacheShardCount]cacheShard[K, V]
}

type cacheShard[K comparable, V any] struct {
	mu    sync.RWMutex
	items map[K]*cacheEntry[K, V]
}

type cacheEntry[K comparable, V any] struct {
	key          K
	value        V
	cost         int64
	expires      time.Time
	index        int
	newer, older *cacheEntry[K, V]
	referenced   atomic.Bool
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
	return &boundedCache[K, V]{items: make(map[K]*cacheEntry[K, V]), maxEntries: entries, maxBytes: bytes, seed: maphash.MakeSeed()}
}

func (c *boundedCache[K, V]) shard(key K) *cacheShard[K, V] {
	return &c.shards[maphash.Comparable(c.seed, key)%cacheShardCount]
}

func (c *boundedCache[K, V]) remove(e *cacheEntry[K, V]) {
	shard := c.shard(e.key)
	shard.mu.Lock()
	delete(shard.items, e.key)
	shard.mu.Unlock()
	c.unlink(e)
	delete(c.items, e.key)
	c.bytes -= e.cost
	heap.Remove(&c.expiry, e.index)
}

func (c *boundedCache[K, V]) get(key K, now time.Time) (V, bool) {
	shard := c.shard(key)
	shard.mu.RLock()
	e := shard.items[key]
	shard.mu.RUnlock()
	// Published key/value/expiry fields never change, even after eviction.
	// Once referenced, repeated hot-key hits need no atomic writes or LRU edits.
	if e != nil && now.Before(e.expires) {
		if !e.referenced.Load() {
			e.referenced.Store(true)
		}
		return e.value, true
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
	if c.maxEntries <= 0 || cost <= 0 || cost > c.maxBytes || !time.Now().Before(expires) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.items[key]; old != nil {
		c.remove(old)
	}
	for len(c.items) >= c.maxEntries || cost > c.maxBytes-c.bytes {
		// Bound the scan even when concurrent readers continuously touch entries.
		for scanned := 0; scanned < len(c.items); scanned++ {
			if !c.oldest.referenced.Swap(false) {
				break
			}
			e := c.oldest
			c.unlink(e)
			c.touch(e)
		}
		c.remove(c.oldest)
	}
	e := &cacheEntry[K, V]{key: key, value: value, cost: cost, expires: expires}
	c.items[key] = e
	shard := c.shard(key)
	shard.mu.Lock()
	if shard.items == nil {
		shard.items = make(map[K]*cacheEntry[K, V])
	}
	shard.items[key] = e
	shard.mu.Unlock()
	c.touch(e)
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

// Links are changed only by writers; hot readers mark a second chance instead.
func (c *boundedCache[K, V]) unlink(e *cacheEntry[K, V]) {
	if e.newer != nil {
		e.newer.older = e.older
	} else {
		c.newest = e.older
	}
	if e.older != nil {
		e.older.newer = e.newer
	} else {
		c.oldest = e.newer
	}
	e.newer, e.older = nil, nil
}
func (c *boundedCache[K, V]) touch(e *cacheEntry[K, V]) {
	e.older = c.newest
	if c.newest != nil {
		c.newest.newer = e
	} else {
		c.oldest = e
	}
	c.newest = e
}
