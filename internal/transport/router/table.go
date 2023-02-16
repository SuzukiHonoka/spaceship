package router

import "sync"

type RoutesTable map[string]Egress

type syncedRoutesTable struct {
	sync.RWMutex
	RoutesTable
}

func (t *syncedRoutesTable) Set(k string, egress Egress) {
	t.Lock()
	t.RoutesTable[k] = egress
	t.Unlock()
}

func (t *syncedRoutesTable) Get(k string) (Egress, bool) {
	t.RLock()
	egress, ok := t.RoutesTable[k]
	t.RUnlock()
	return egress, ok
}
