package router

import (
	"container/list"
	"sync"
)

const maxCacheSize = 10000 // Maximum number of cached routes

type cacheEntry struct {
	key    string
	egress Egress
}

type syncedRoutesTable struct {
	mu      sync.Mutex
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

	// Evict the oldest if at capacity
	if t.lruList.Len() >= t.maxSize {
		oldest := t.lruList.Back()
		if oldest != nil {
			entry := oldest.Value.(*cacheEntry)
			delete(t.cache, entry.key)
			t.lruList.Remove(oldest)
		}
	}

	// Add new entry
	entry := &cacheEntry{key: k, egress: egress}
	elem := t.lruList.PushFront(entry)
	t.cache[k] = elem
}

func (t *syncedRoutesTable) Get(k string) (Egress, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if elem, exists := t.cache[k]; exists {
		t.lruList.MoveToFront(elem)
		return elem.Value.(*cacheEntry).egress, true
	}
	return EgressUnknown, false
}

func (t *syncedRoutesTable) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cache = make(map[string]*list.Element)
	t.lruList.Init()
}
