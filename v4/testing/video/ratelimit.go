package video

import (
	"time"
)

type RateLimiter struct {
	bps float64

	tokens float64
	last   time.Time
}

func NewRateLimiter(mbps float64) *RateLimiter {
	if mbps <= 0 {
		return &RateLimiter{}
	}
	return &RateLimiter{
		bps:  mbps * 1e6 / 8.0,
		last: time.Now(),
	}
}

func (r *RateLimiter) Wait(n int) {
	if r == nil || r.bps <= 0 || n <= 0 {
		return
	}
	now := time.Now()
	if r.last.IsZero() {
		r.last = now
	}
	elapsed := now.Sub(r.last).Seconds()
	r.last = now
	r.tokens += elapsed * r.bps
	maxBurst := r.bps * 0.2
	if r.tokens > maxBurst {
		r.tokens = maxBurst
	}
	need := float64(n)
	if r.tokens >= need {
		r.tokens -= need
		return
	}
	deficit := need - r.tokens
	r.tokens = 0
	sleep := time.Duration(deficit / r.bps * float64(time.Second))
	if sleep > 0 {
		time.Sleep(sleep)
	}
}
