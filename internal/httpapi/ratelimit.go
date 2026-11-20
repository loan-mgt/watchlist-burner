package httpapi

import (
	"sync"
	"time"
)

// limiter is a per-key token bucket. Keys are client IPs (or IP + token hash).
// The map is swept lazily so an attacker rotating addresses cannot grow it
// without bound.
type limiter struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	burst    float64
	buckets  map[string]*bucket
	lastSeen time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perMinute int) *limiter {
	if perMinute <= 0 {
		return nil // disabled
	}
	rate := float64(perMinute) / 60
	return &limiter{
		rate:    rate,
		burst:   float64(perMinute),
		buckets: make(map[string]*bucket),
	}
}

// allow consumes one token for key, reporting how long to wait when denied.
func (l *limiter) allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSeen) > time.Minute {
		l.sweep(now)
		l.lastSeen = now
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
		return false, wait
	}
	b.tokens--
	return true, 0
}

// sweep drops buckets that have refilled completely.
func (l *limiter) sweep(now time.Time) {
	if len(l.buckets) < 10000 {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, k)
		}
	}
}
