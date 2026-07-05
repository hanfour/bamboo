// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ratelimit is a small in-memory per-key token-bucket limiter for
// brute-force protection on the controller's public endpoints (login,
// pre-auth-key redemption, relay-token minting) — audit finding H-4.
//
// In-memory is sufficient for the single-VPS production topology (one
// controller instance). The Limiter interface is deliberately minimal so
// a Redis-backed implementation can replace it for a multi-instance
// deployment without touching call sites.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a token-bucket rate limiter keyed by an arbitrary string
// (e.g. "<category>:<client-ip>"). Each key gets its own bucket that
// starts full (burst) and refills at `rate` tokens/second up to `burst`.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64 // bucket capacity
	// now is injectable so tests can advance time deterministically.
	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a Limiter allowing `burst` requests immediately and then
// `ratePerMinute` sustained requests per minute per key. A zero or
// negative ratePerMinute/burst is clamped to a safe minimum so a
// misconfiguration can't accidentally disable limiting.
func New(ratePerMinute, burst int) *Limiter {
	if ratePerMinute < 1 {
		ratePerMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    float64(ratePerMinute) / 60.0,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consumes one token for key and reports whether the request is
// permitted. It refills the key's bucket based on elapsed time first.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	// Refill for the elapsed interval, capped at burst.
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// RetryAfter returns how long the caller should wait before a token for
// key becomes available. Zero when a token is available now. Callers use
// it for the Retry-After response header.
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil || b.tokens >= 1 {
		return 0
	}
	need := 1 - b.tokens
	return time.Duration(need/l.rate*float64(time.Second)) + time.Millisecond
}

// Cleanup evicts buckets that have fully refilled and been idle for at
// least idleFor — bounding memory under a churn of distinct client IPs.
// A full bucket is indistinguishable from a never-seen key, so dropping
// it is safe. Call it periodically from a background goroutine.
func (l *Limiter) Cleanup(idleFor time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, b := range l.buckets {
		idle := now.Sub(b.last)
		// Refill projection: full again after (burst-tokens)/rate seconds.
		refilled := b.tokens+idle.Seconds()*l.rate >= l.burst
		if refilled && idle >= idleFor {
			delete(l.buckets, key)
		}
	}
}

// Len reports the number of tracked keys. Test/observability helper.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
