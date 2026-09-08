package middleware

import (
	"fmt"
	"testing"
	"time"
)

func TestCacheEvictionBoundsSecondChanceWork(t *testing.T) {
	for _, cost := range []int64{1, 256} {
		t.Run(fmt.Sprint(cost), func(t *testing.T) {
			const count = 512
			cache := newBoundedCache[int, int](count, count)
			defer cache.clear()
			expires := time.Now().Add(time.Hour)
			for i := 0; i < count; i++ {
				cache.put(i, i, 1, expires)
				cache.get(i, time.Now())
			}
			cache.put(count, count, cost, expires)
			cache.mu.Lock()
			cleared := 0
			for key, entry := range cache.items {
				if key != count && !entry.referenced.Load() {
					cleared++
				}
			}
			items, retained := len(cache.items), cache.bytes
			heapEntries := len(cache.expiry)
			cache.mu.Unlock()
			if cleared > cacheEvictionScanLimit {
				t.Fatalf("rotated %d hot entries in one insertion, budget %d", cleared, cacheEvictionScanLimit)
			}
			if items != count-int(cost)+1 || retained != count || heapEntries != items {
				t.Fatalf("eviction violated capacity/index accounting: items=%d bytes=%d expiry=%d", items, retained, heapEntries)
			}
			if value, ok := cache.get(count, time.Now()); !ok || value != count {
				t.Fatal("insertion lost while enforcing the scan limit")
			}
		})
	}
}

func TestCacheEvictionStillGivesHotEntriesASecondChance(t *testing.T) {
	cache := newBoundedCache[string, string](3, 3)
	defer cache.clear()
	expires := time.Now().Add(time.Hour)
	for _, key := range []string{"hot", "cold", "newer"} {
		cache.put(key, key, 1, expires)
	}
	cache.get("hot", time.Now())
	cache.put("incoming", "incoming", 1, expires)
	if _, ok := cache.get("hot", time.Now()); !ok {
		t.Fatal("recently read entry lost its second chance")
	}
	if _, ok := cache.get("cold", time.Now()); ok {
		t.Fatal("cold candidate was not evicted")
	}
}
