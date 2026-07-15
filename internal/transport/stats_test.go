package transport

import (
	"testing"
	"time"
)

func TestStatsCalculateSpeedUsesStableSamplingWindow(t *testing.T) {
	s := new(stats)
	start := time.Unix(100, 0)

	s.sampleAt(start)
	if tx, rx := s.CalculateSpeed(); tx != 0 || rx != 0 {
		t.Fatalf("initial speed = (%v, %v), want (0, 0)", tx, rx)
	}

	s.AddTx(100)
	s.AddRx(200)
	s.sampleAt(start.Add(time.Second))
	tx, rx := s.CalculateSpeed()
	if tx != 100 || rx != 200 {
		t.Fatalf("first sampled speed = (%v, %v), want (100, 200)", tx, rx)
	}

	// Readers only observe the cached sample and cannot advance the shared
	// byte/time baseline.
	s.AddTx(50)
	s.AddRx(100)
	tx, rx = s.CalculateSpeed()
	if tx != 100 || rx != 200 {
		t.Fatalf("cached speed = (%v, %v), want (100, 200)", tx, rx)
	}

	s.sampleAt(start.Add(2 * time.Second))
	tx, rx = s.CalculateSpeed()
	if tx != 50 || rx != 100 {
		t.Fatalf("next sampled speed = (%v, %v), want (50, 100)", tx, rx)
	}
}

func TestStatsCalculateSpeedHandlesClockRegression(t *testing.T) {
	s := new(stats)
	start := time.Unix(100, 0)
	s.sampleAt(start)
	s.AddTx(100)

	s.sampleAt(start.Add(-time.Second))
	if tx, rx := s.CalculateSpeed(); tx != 0 || rx != 0 {
		t.Fatalf("speed after clock regression = (%v, %v), want cached zeros", tx, rx)
	}
	s.sampleAt(start.Add(time.Second))
	if tx, _ := s.CalculateSpeed(); tx != 100 {
		t.Fatalf("clock regression advanced baseline: tx speed = %v, want 100", tx)
	}
}
