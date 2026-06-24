package router

import (
	"container/list"
	"sync"
	"sync/atomic"
)

const maxCacheSize = 10000 // Maximum number of cached routes

type cacheEntry struct {
	key    string
	egress Egress
	// referenced is the CLOCK "second-chance" bit: set on every Get (under a
	// read lock) and consulted/cleared only during eviction (under the write
	// lock). It lets reads avoid taking the write lock just to record recency.
	referenced atomic.Bool
}

type syncedRoutesTable struct {
	mu      sync.RWMutex
	cache   map[string]*list.Element
	lruList *list.List
	maxSize int
}

func newSyncedRoutesTable(maxSize int) *syncedRoutesTable {
	if maxSize <= 0 {
		maxSize = maxCacheSize
	}
	return &syncedRoutesTable{
		cache:   make(map[string]*list.Element),
		lruList: list.New(),
		maxSize: maxSize,
	}
}

func (t *syncedRoutesTable) Set(k string, egress Egress) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// If key exists, update and move to front
	if elem, exists := t.cache[k]; exists {
		t.lruList.MoveToFront(elem)
		elem.Value.(*cacheEntry).egress = egress
		return
	}

	// Evict using a second-chance (CLOCK) scan from the LRU end: an entry that
	// has been referenced since it was last considered gets one more chance
	// (its bit is cleared and it is moved to the front); the first unreferenced
	// entry is evicted.
	if t.lruList.Len() >= t.maxSize {
		for e := t.lruList.Back(); e != nil; e = t.lruList.Back() {
			entry := e.Value.(*cacheEntry)
			if entry.referenced.Load() {
				entry.referenced.Store(false)
				t.lruList.MoveToFront(e)
				continue
			}
			delete(t.cache, entry.key)
			t.lruList.Remove(e)
			break
		}
	}

	// Add new entry
	entry := &cacheEntry{key: k, egress: egress}
	elem := t.lruList.PushFront(entry)
	t.cache[k] = elem
}

func (t *syncedRoutesTable) Get(k string) (Egress, bool) {
	// Read lock only: lookups run concurrently. Recency is recorded by setting
	// the entry's atomic referenced bit instead of reordering the list (which
	// would require the write lock and serialize every lookup).
	t.mu.RLock()
	defer t.mu.RUnlock()

	elem, exists := t.cache[k]
	if !exists {
		return EgressUnknown, false
	}
	entry := elem.Value.(*cacheEntry)
	entry.referenced.Store(true)
	return entry.egress, true
}

func (t *syncedRoutesTable) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cache = make(map[string]*list.Element)
	t.lruList.Init()
}
