package client

import (
	"runtime"
	"testing"
	"time"
)

func TestNextWarmDelay(t *testing.T) {
	stable := time.Now().Add(-2 * warmStableAfter)
	for range 100 {
		if d := nextWarmDelay(10*time.Second, stable); d < 0 || d >= warmSpread {
			t.Fatalf("stable connection waited %v, want under %v", d, warmSpread)
		}
	}

	flapped := time.Now().Add(-time.Second)
	delay := time.Duration(0)
	for step := range 10 {
		next := nextWarmDelay(delay, flapped)
		ceiling := min(warmBaseDelay<<step, warmMaxDelay)
		if next < ceiling/2 || next >= ceiling {
			t.Fatalf("step %d: delay %v, want within [%v, %v)", step, next, ceiling/2, ceiling)
		}
		// Feed back the ceiling the way a full delay would, to walk the curve.
		delay = ceiling
	}

	if d := nextWarmDelay(0, time.Time{}); d < warmBaseDelay/2 || d >= warmBaseDelay {
		t.Fatalf("never-ready connection waited %v, want the base backoff", d)
	}
}

func TestIdleByConfiguration(t *testing.T) {
	w := &ConnWrapper{}
	w.lastActive.Store(time.Now().Add(-time.Minute).UnixNano())
	if w.idleByConfiguration(0) {
		t.Fatal("no idle timeout configured, yet idleness was treated as deliberate")
	}
	if !w.idleByConfiguration(30 * time.Second) {
		t.Fatal("unused for twice the idle timeout, yet not treated as deliberate idleness")
	}
	if w.idleByConfiguration(5 * time.Minute) {
		t.Fatal("used within the idle timeout, yet treated as deliberate idleness")
	}
	w.Use()
	if w.idleByConfiguration(time.Nanosecond) {
		t.Fatal("a connection with live reservations was treated as idle")
	}
}

func TestKeepWarmStopsWhenClosed(t *testing.T) {
	baseline := runtime.NumGoroutine()
	w := newQueueTestWrapper(t, 1)
	w.Connect()
	w.startKeepWarm(0)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after Close, want %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
