package netmon

import (
	"errors"
	"testing"
)

func TestMonitorLifecycleAndExclude(t *testing.T) {
	mon, err := NewMonitor("spaceship0")
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	if err := mon.Start(func(DefaultInterface) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := mon.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	cur, err := mon.Current()
	if err != nil {
		if errors.Is(err, ErrNoDefaultInterface) {
			t.Skip("host has no default route in this environment")
		}
		t.Fatalf("Current: %v", err)
	}
	if cur.Index == 0 || cur.Name == "" {
		t.Fatalf("invalid default interface: %+v", cur)
	}
	if cur.Name == "spaceship0" {
		t.Fatalf("default interface must not return excluded TUN %q", cur.Name)
	}
}
