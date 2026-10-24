// Package ratelimit is a small in-memory token-bucket limiter keyed by
// string (an IP, an Invite). State is lost on restart, which is fine for
// slowing down abuse of a single-Owner instance.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows Burst events at once per key, refilling one event every
// Every.
type Limiter struct {
	Burst int
	Every time.Duration
	Now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func New(burst int, every time.Duration) *Limiter {
	return &Limiter{Burst: burst, Every: every, Now: time.Now, buckets: map[string]*bucket{}}
}

// Allow reports whether key may act now, spending a token if so.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.Burst), last: now}
		l.buckets[key] = b
	}
	b.tokens = min(float64(l.Burst), b.tokens+float64(now.Sub(b.last))/float64(l.Every))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets that have refilled completely, so memory stays bounded
// by recently active keys.
func (l *Limiter) sweep(now time.Time) {
	full := time.Duration(l.Burst) * l.Every
	if now.Sub(l.swept) < full {
		return
	}
	l.swept = now
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
}
