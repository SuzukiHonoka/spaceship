package transport

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type Direction string

const (
	DirectionOut        Direction = "out"
	DirectionIn         Direction = "in"
	statsSampleInterval           = time.Second
)

var GlobalStats = newStats()

type stats struct {
	tx              atomic.Uint64
	rx              atomic.Uint64
	mu              sync.Mutex
	lastTx          uint64
	lastRx          uint64
	lastCalculation time.Time
	initialized     bool
	lastTxSpeed     atomic.Uint64 // last computed tx speed bits (math.Float64bits)
	lastRxSpeed     atomic.Uint64 // last computed rx speed bits (math.Float64bits)
}

func newStats() *stats {
	s := new(stats)
	s.sampleAt(time.Now())
	go s.sampleLoop()
	return s
}

func (s *stats) sampleLoop() {
	ticker := time.NewTicker(statsSampleInterval)
	for currentTime := range ticker.C {
		s.sampleAt(currentTime)
	}
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
	return math.Float64frombits(s.lastTxSpeed.Load()), math.Float64frombits(s.lastRxSpeed.Load())
}

func (s *stats) sampleAt(currentTime time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Initialize on first call
	if !s.initialized {
		s.lastCalculation = currentTime
		s.lastTx = s.tx.Load()
		s.lastRx = s.rx.Load()
		s.initialized = true
		return
	}

	elapsed := currentTime.Sub(s.lastCalculation)
	if elapsed <= 0 {
		return
	}
	duration := elapsed.Seconds()
	s.lastCalculation = currentTime

	// Calculate the speed
	currentTx := s.tx.Load()
	currentRx := s.rx.Load()
	lastTx := s.lastTx
	lastRx := s.lastRx
	var txSpeed, rxSpeed float64

	if currentTx > lastTx {
		txSpeed = float64(currentTx-lastTx) / duration
	}
	if currentRx > lastRx {
		rxSpeed = float64(currentRx-lastRx) / duration
	}

	s.lastTx = currentTx
	s.lastRx = currentRx
	s.lastTxSpeed.Store(math.Float64bits(txSpeed))
	s.lastRxSpeed.Store(math.Float64bits(rxSpeed))
}
