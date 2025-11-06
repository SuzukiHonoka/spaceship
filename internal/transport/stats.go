package transport

import (
	"sync"
	"sync/atomic"
	"time"
)

type Direction string

const (
	DirectionOut Direction = "out"
	DirectionIn  Direction = "in"
)

var GlobalStats = new(stats)

type stats struct {
	tx              atomic.Uint64
	rx              atomic.Uint64
	lastTx          atomic.Uint64
	lastRx          atomic.Uint64
	lastCalculation time.Time
	mu              sync.Mutex
}

func (s *stats) AddTx(bytes uint64) {
	s.tx.Add(bytes)
}

func (s *stats) AddRx(bytes uint64) {
	s.rx.Add(bytes)
}

func (s *stats) Total() (tx uint64, rx uint64) {
	tx, rx = s.tx.Load(), s.rx.Load()
	return tx, rx
}

func (s *stats) CalculateSpeed() (txSpeed float64, rxSpeed float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get the current time
	now := time.Now()

	// Bypass first call
	if s.lastCalculation.IsZero() {
		s.lastCalculation = now
		s.lastTx.Store(s.tx.Load())
		s.lastRx.Store(s.rx.Load())
		return 0, 0
	}

	// Calculate the time difference in seconds
	duration := now.Sub(s.lastCalculation).Seconds()
	if duration == 0 {
		return 0, 0 // Prevent division by zero
	}
	s.lastCalculation = now

	// Calculate the speed
	currentTx := s.tx.Load()
	currentRx := s.rx.Load()
	lastTx := s.lastTx.Load()
	lastRx := s.lastRx.Load()

	if currentTx > lastTx {
		txSpeed = float64(currentTx-lastTx) / duration
	}
	if currentRx > lastRx {
		rxSpeed = float64(currentRx-lastRx) / duration
	}

	s.lastTx.Store(currentTx)
	s.lastRx.Store(currentRx)
	return txSpeed, rxSpeed
}
