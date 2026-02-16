package video

import (
	"math/rand"
	"sync"
	"time"
)

type Simulator struct {
	BaseDelay time.Duration
	JitterMax time.Duration
	DropPct   int

	mu  sync.Mutex
	rnd *rand.Rand
}

func NewSimulator(seed int64, baseDelay, jitterMax time.Duration, dropPct int) *Simulator {
	if dropPct < 0 {
		dropPct = 0
	}
	if dropPct > 100 {
		dropPct = 100
	}
	return &Simulator{
		BaseDelay: baseDelay,
		JitterMax: jitterMax,
		DropPct:   dropPct,
		rnd:       rand.New(rand.NewSource(seed)),
	}
}

func (s *Simulator) ShouldDrop() bool {
	if s.DropPct <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rnd.Intn(100) < s.DropPct
}

func (s *Simulator) NextDelay() time.Duration {
	d := s.BaseDelay
	if s.JitterMax <= 0 {
		return d
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j := time.Duration(s.rnd.Int63n(int64(s.JitterMax) + 1))
	return d + j
}
