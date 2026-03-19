package transport

import (
	"math"
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
	lastCalculation atomic.Int64 // UnixNano timestamp
	mu              sync.Mutex
	initialized     atomic.Bool
	lastTxSpeed     atomic.Uint64 // last computed tx speed bits (math.Float64bits)
	lastRxSpeed     atomic.Uint64 // last computed rx speed bits (math.Float64bits)
}

func (s *stats) AddTx(bytes uint64) {
	s.tx.Add(bytes)
}

func (s *stats) AddRx(bytes uint64) {
	s.rx.Add(bytes)
}

func (s *stats) Add(direction Direction, bytes int64) {
	if bytes <= 0 {
		return
	}
	n := uint64(bytes)

	switch direction {
	case DirectionIn:
		s.AddRx(n)
	case DirectionOut:
		s.AddTx(n)
	}
}

func (s *stats) Total() (tx uint64, rx uint64) {
	return s.tx.Load(), s.rx.Load()
}

func (s *stats) CalculateSpeed() (txSpeed float64, rxSpeed float64) {
	// Use TryLock to avoid blocking - if locked, return cached values
	if !s.mu.TryLock() {
		return math.Float64frombits(s.lastTxSpeed.Load()), math.Float64frombits(s.lastRxSpeed.Load())
	}
	defer s.mu.Unlock()

	now := time.Now().UnixNano()
	lastCalc := s.lastCalculation.Load()

	// Initialize on first call
	if !s.initialized.Load() {
		s.lastCalculation.Store(now)
		s.lastTx.Store(s.tx.Load())
		s.lastRx.Store(s.rx.Load())
		s.initialized.Store(true)
		return 0, 0
	}

	// Calculate the time difference in seconds
	duration := float64(now-lastCalc) / float64(time.Second)
	if duration <= 0 {
		return math.Float64frombits(s.lastTxSpeed.Load()), math.Float64frombits(s.lastRxSpeed.Load())
	}
	s.lastCalculation.Store(now)

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
	s.lastTxSpeed.Store(math.Float64bits(txSpeed))
	s.lastRxSpeed.Store(math.Float64bits(rxSpeed))
	return txSpeed, rxSpeed
}
