// SPDX-License-Identifier: AGPL-3.0-or-later

package ratelimit

import (
	"testing"
	"time"
)

// clock is a manually-advanced time source for deterministic refill tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newWithClock(rpm, burst int, c *clock) *Limiter {
	l := New(rpm, burst)
	l.now = c.now
	return l
}

func TestLimiter_AllowsBurstThenDenies(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newWithClock(60, 3, c) // 1/sec sustained, burst 3
	key := "auth:1.2.3.4"

	for i := 0; i < 3; i++ {
		if !l.Allow(key) {
			t.Fatalf("request %d within burst was denied", i+1)
		}
	}
	if l.Allow(key) {
		t.Fatal("4th request within the same instant should be denied (burst exhausted)")
	}
}

func TestLimiter_RefillsOverTime(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newWithClock(60, 2, c) // 1 token/sec, burst 2
	key := "k"

	if !l.Allow(key) || !l.Allow(key) {
		t.Fatal("burst of 2 should pass")
	}
	if l.Allow(key) {
		t.Fatal("3rd immediate request should be denied")
	}
	c.add(1100 * time.Millisecond) // ~1.1 tokens refilled
	if !l.Allow(key) {
		t.Fatal("after ~1s one token should be available")
	}
	if l.Allow(key) {
		t.Fatal("only one token refilled; second should be denied")
	}
}

func TestLimiter_KeysAreIndependent(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newWithClock(60, 1, c)
	if !l.Allow("a") {
		t.Fatal("first for a should pass")
	}
	if !l.Allow("b") {
		t.Fatal("first for b should pass — keys must not share a bucket")
	}
	if l.Allow("a") {
		t.Fatal("second for a should be denied")
	}
}

func TestLimiter_RetryAfterIsPositiveWhenLimited(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newWithClock(60, 1, c) // 1/sec
	l.Allow("k")                // consume the single token
	if l.Allow("k") {
		t.Fatal("should be limited")
	}
	ra := l.RetryAfter("k")
	if ra <= 0 || ra > 2*time.Second {
		t.Errorf("RetryAfter = %v, want ~1s", ra)
	}
	// An unseen key is not limited.
	if l.RetryAfter("other") != 0 {
		t.Error("unseen key should have zero RetryAfter")
	}
}

func TestLimiter_CleanupEvictsIdleRefilledBuckets(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newWithClock(60, 2, c)
	l.Allow("k") // creates a bucket, now at 1 token
	if l.Len() != 1 {
		t.Fatalf("expected 1 bucket, got %d", l.Len())
	}
	// Idle long enough to fully refill AND exceed the idle threshold.
	c.add(5 * time.Minute)
	l.Cleanup(time.Minute)
	if l.Len() != 0 {
		t.Errorf("idle refilled bucket should be evicted, still have %d", l.Len())
	}
}

func TestLimiter_ClampsBadConfig(t *testing.T) {
	l := New(0, 0) // both invalid → clamped to >=1
	if !l.Allow("k") {
		t.Fatal("clamped burst should allow the first request")
	}
	if l.Allow("k") {
		t.Fatal("clamped burst of 1 should deny the second immediate request")
	}
}
