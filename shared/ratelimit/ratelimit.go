// Package ratelimit provides the per-key request limiter guarding every
// public surface (basic abuse control) — the Relay, and a
// Companion serving its own endpoints behind a tunnel. Keys are device IDs or
// client IPs.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxIdle is how long an untouched bucket survives; sweepAt bounds the map so
// an attacker rotating keys cannot grow it without limit.
const (
	maxIdle = 10 * time.Minute
	sweepAt = 4096
)

// Limiter hands out one token bucket per key.
type Limiter struct {
	perSecond rate.Limit
	burst     int

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// New returns a Limiter allowing perSecond sustained requests with the given
// burst per key.
func New(perSecond float64, burst int) *Limiter {
	return &Limiter{
		perSecond: rate.Limit(perSecond),
		burst:     burst,
		buckets:   map[string]*bucket{},
		now:       time.Now,
	}
}

// Allow reports whether one more request for key fits the budget.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= sweepAt {
			l.sweep(now)
		}
		b = &bucket{lim: rate.NewLimiter(l.perSecond, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	l.mu.Unlock()
	return b.lim.Allow()
}

// sweep drops idle buckets. Called with l.mu held, only when the map is at
// capacity, so steady-state traffic never pays for it.
func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > maxIdle {
			delete(l.buckets, k)
		}
	}
}
