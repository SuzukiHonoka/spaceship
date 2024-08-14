package router

import "sync"

type RoutesTable map[string]Egress

type syncedRoutesTable struct {
	mu sync.RWMutex
	RoutesTable
}

func (t *syncedRoutesTable) Set(k string, egress Egress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.RoutesTable[k] = egress
}

func (t *syncedRoutesTable) Get(k string) (Egress, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	egress, ok := t.RoutesTable[k]
	return egress, ok
}
