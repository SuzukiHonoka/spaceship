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
	s.lastCalculation = now

	// Calculate the speed
	currentTx := s.tx.Load()
	currentRx := s.rx.Load()

	if currentTx > 0 {
		txSpeed = float64(currentTx-s.lastTx.Load()) / duration
	}
	if currentRx > 0 {
		rxSpeed = float64(currentRx-s.lastRx.Load()) / duration
	}

	s.lastTx.Store(currentTx)
	s.lastRx.Store(currentRx)
	return txSpeed, rxSpeed
}
