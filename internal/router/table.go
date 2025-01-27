package router

import "sync"

type RoutesTable map[string]Egress

type syncedRoutesTable struct {
	m sync.Map
}

func (t *syncedRoutesTable) Set(k string, egress Egress) {
	t.m.Store(k, egress)
}

func (t *syncedRoutesTable) Get(k string) (Egress, bool) {
	v, ok := t.m.Load(k)
	if !ok {
		return EgressUnknown, false
	}
	egress, _ := v.(Egress)
	return egress, true
}
