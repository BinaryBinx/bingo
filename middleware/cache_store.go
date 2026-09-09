package middleware

import (
	"container/heap"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

const cacheShardCount = 32

// One insertion can rotate at most this many recently referenced candidates.
// Capacity enforcement may still need multiple evictions for a large value.
const cacheEvictionScanLimit = 64

type cacheGroupKey struct{ host, uri string }

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
	// Optional intrusive index: one head per retained Host/URI, with no entries
	// for absent URLs. All membership changes share the store's writer lock.
	groupOf func(K) cacheGroupKey
	groups  map[cacheGroupKey]*cacheEntry[K, V]
}

type cacheShard[K comparable, V any] struct {
	mu    sync.RWMutex
	items map[K]*cacheEntry[K, V]
}

type cacheEntry[K comparable, V any] struct {
	key                      K
	value                    V
	cost                     int64
	expires                  time.Time
	index                    int
	newer, older             *cacheEntry[K, V]
	referenced               atomic.Bool
	groupNext, groupPrevious *cacheEntry[K, V]
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
	c.removeMetadata(e)
}

// The writer lock protects bookkeeping separately from publication. Replacement
// removes only these links, keeping the old immutable snapshot visible until a
// new snapshot is ready to replace its shard entry in one operation.
func (c *boundedCache[K, V]) removeMetadata(e *cacheEntry[K, V]) {
	c.unlink(e)
	if c.groupOf != nil {
		group := c.groupOf(e.key)
		if e.groupPrevious != nil {
			e.groupPrevious.groupNext = e.groupNext
		} else if e.groupNext != nil {
			c.groups[group] = e.groupNext
		} else {
			delete(c.groups, group)
		}
		if e.groupNext != nil {
			e.groupNext.groupPrevious = e.groupPrevious
		}
		e.groupNext, e.groupPrevious = nil, nil
	}
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

// deleteGroup visits only the target's retained variants and Vary schema. Eviction,
// replacement and expiry remove index membership through the same remove method.
func (c *boundedCache[K, V]) deleteGroup(group cacheGroupKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.groups[group]
	if entry == nil {
		return
	}
	for entry != nil {
		next := entry.groupNext
		c.remove(entry)
		entry = next
	}
	c.scheduleExpiry()
}

func (c *boundedCache[K, V]) put(key K, value V, cost int64, expires time.Time) {
	if c.maxEntries <= 0 || cost <= 0 || cost > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, value, cost, expires)
}

// putLocked publishes an immutable entry while the caller holds the writer lock.
// Keeping the same publication path lets schema updates inspect the old expiry
// atomically without changing the allocation-free reader path.
func (c *boundedCache[K, V]) putLocked(key K, value V, cost int64, expires time.Time) {
	if c.maxEntries <= 0 || cost <= 0 || cost > c.maxBytes {
		return
	}
	// A queued insertion must not replace a valid entry with an already expired
	// snapshot after waiting for the writer lock.
	if !time.Now().Before(expires) {
		return
	}
	if old := c.items[key]; old != nil {
		c.removeMetadata(old)
	}
	scanBudget := min(cacheEvictionScanLimit, len(c.items))
	for len(c.items) >= c.maxEntries || cost > c.maxBytes-c.bytes {
		// Share the scan budget across every eviction in this insertion. Once
		// exhausted, evict the oldest candidate even if it was referenced. This
		// trades a little recency precision for bounded second-chance work under
		// the global writer lock, including when all entries are hot.
		for scanBudget > 0 {
			scanBudget--
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
	if c.groupOf != nil {
		if c.groups == nil {
			c.groups = make(map[cacheGroupKey]*cacheEntry[K, V])
		}
		group := c.groupOf(key)
		e.groupNext = c.groups[group]
		if e.groupNext != nil {
			e.groupNext.groupPrevious = e
		}
		c.groups[group] = e
	}
	c.touch(e)
	c.bytes += cost
	heap.Push(&c.expiry, e)
	shard := c.shard(key)
	shard.mu.Lock()
	if shard.items == nil {
		shard.items = make(map[K]*cacheEntry[K, V])
	}
	shard.items[key] = e
	shard.mu.Unlock()
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

// clear releases all retained snapshots and stops idle expiry scheduling.
// A callback already queued by time.Timer observes the empty heap under mu.
func (c *boundedCache[K, V]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		shard.items = nil
		shard.mu.Unlock()
	}
	c.items = make(map[K]*cacheEntry[K, V])
	c.groups = nil
	c.expiry, c.newest, c.oldest = nil, nil, nil
	c.bytes = 0
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
