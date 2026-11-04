package server

import (
	"sync"
	"time"
)

// rlSweepInterval is how often idle buckets are discarded, so a long-running
// server does not accumulate one entry per client address forever.
const rlSweepInterval = 10 * time.Minute

// rlMaxBuckets bounds the table between sweeps. A flood of forged remote
// addresses cannot grow the limiter without limit.
const rlMaxBuckets = 4096

// bucket is one client's token bucket.
type bucket struct {
	// tokens is the credit left, in requests.
	tokens float64
	// last is when tokens was computed.
	last time.Time
}

// rateLimiter is a per-key token bucket.
//
// The key is the client address. A forged X-Forwarded-For is deliberately not
// honoured: ccresu binds loopback and never sits behind a trusted proxy, so a
// header would only let a client pick its own bucket.
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
	swept   time.Time
}

// newRateLimiter builds a limiter allowing rate requests per second with an
// initial burst.
func newRateLimiter(rate float64, burst int) *rateLimiter {
	return &rateLimiter{
		rate:    rate,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
	}
}

// allow reports whether the key may proceed now, and how long to wait when it
// may not.
func (l *rateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	entry, ok := l.buckets[key]
	if !ok {
		entry = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = entry
		if len(l.buckets) > rlMaxBuckets {
			// The sweep can only bring the table back to the cap before this insert,
			// so make room here. The client being served keeps its bucket; an
			// arbitrary older one goes, which costs its owner at most a fresh burst.
			for victim := range l.buckets {
				if victim != key {
					delete(l.buckets, victim)
					break
				}
			}
		}
	}
	// Refill: whole elapsed seconds only, so the arithmetic never underflows and
	// a clock that steps backwards cannot hand out free credit.
	if elapsed := now.Sub(entry.last); elapsed > 0 {
		entry.tokens += elapsed.Seconds() * l.rate
		if entry.tokens > l.burst {
			entry.tokens = l.burst
		}
		entry.last = now
	}
	if entry.tokens >= 1 {
		entry.tokens--
		return true, 0
	}
	needed := 1 - entry.tokens
	wait := time.Duration(needed / l.rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Second
	}
	return false, wait
}

// sweep drops buckets that are old enough to have refilled completely and, if the
// table is oversized, everything idle.
func (l *rateLimiter) sweep(now time.Time) {
	tooMany := len(l.buckets) > rlMaxBuckets
	if !tooMany && now.Sub(l.swept) < rlSweepInterval {
		return
	}
	l.swept = now
	// Full refill takes burst/rate seconds; anything older carries no information.
	idleAfter := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Second
	for key, entry := range l.buckets {
		if now.Sub(entry.last) > idleAfter {
			delete(l.buckets, key)
		}
	}
	if tooMany {
		// A burst of distinct clients can leave the table oversized even after the
		// idle sweep: with a coarse clock every entry may carry the same timestamp,
		// so nothing looks idle. Drop entries in any order until the table is back
		// under the cap. Eviction fairness matters less than the guarantee that
		// memory stays bounded, and any evicted client simply starts with a full
		// bucket again.
		for key := range l.buckets {
			if len(l.buckets) <= rlMaxBuckets {
				break
			}
			delete(l.buckets, key)
		}
	}
}
