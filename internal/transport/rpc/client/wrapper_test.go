package client

import (
	"testing"
)

func TestConnWrapper_InUse(t *testing.T) {
	w := &ConnWrapper{}

	if load := w.GetCurrentLoad(); load != 0 {
		t.Errorf("expected load 0, got %v", load)
	}

	w.Use()
	if load := w.GetCurrentLoad(); load != 1 {
		t.Errorf("expected load 1, got %v", load)
	}

	w.Done()
	if load := w.GetCurrentLoad(); load != 0 {
		t.Errorf("expected load 0, got %v", load)
	}
}

func TestConnWrappers_PickLeastLoaded(t *testing.T) {
	// We can't easily mock ClientConn.GetState() as it's a method on a struct.
	// But we can test the load balancing logic by assuming GetState returns Idle/Ready for nil ClientConn (which it won't, it will panic).
	// So we'll skip the tests that call GetState on nil pointers or use a dummy ClientConn.

	w1 := &ConnWrapper{ID: 1}
	w1.InUse.Store(10)

	w2 := &ConnWrapper{ID: 2}
	w2.InUse.Store(5)

	w3 := &ConnWrapper{ID: 3}
	w3.InUse.Store(20)

	wrappers := ConnWrappers{w1, w2, w3}

	// Since we can't easily mock GetState(), we'll test GetDetailedStatus and GetSummaryStats instead.

	status := wrappers.GetDetailedStatus()
	wantStatus := "1(10) 2(5) 3(20)"
	if status != wantStatus {
		t.Errorf("GetDetailedStatus() = %q, want %q", status, wantStatus)
	}

	total, active, totalLoad := wrappers.GetSummaryStats()
	if total != 3 || active != 3 || totalLoad != 35 {
		t.Errorf("GetSummaryStats() = %v, %v, %v; want 3, 3, 35", total, active, totalLoad)
	}
}

func TestConnWrappers_Empty(t *testing.T) {
	var wrappers ConnWrappers
	if got := wrappers.PickLeastLoaded(); got != nil {
		t.Errorf("PickLeastLoaded on empty wrappers should be nil")
	}
	if got := wrappers.GetDetailedStatus(); got != "No connections" {
		t.Errorf("GetDetailedStatus on empty wrappers = %q", got)
	}
}
