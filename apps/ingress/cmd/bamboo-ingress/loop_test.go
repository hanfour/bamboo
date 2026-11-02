// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunEvery_TicksUntilCancelled(t *testing.T) {
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runEvery(ctx, 5*time.Millisecond, func() { n.Add(1) }); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n.Load() < 2 {
		t.Fatalf("fn ran %d times, want >= 2", n.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runEvery did not stop on cancel")
	}
	after := n.Load()
	time.Sleep(30 * time.Millisecond)
	if n.Load() != after {
		t.Error("fn kept running after runEvery returned")
	}
}
