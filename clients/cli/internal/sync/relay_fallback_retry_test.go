// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/device"
)

type retryStats struct{}

func (retryStats) Stats() ([]device.PeerStats, error) {
	return []device.PeerStats{{PublicKey: "peer"}}, nil
}

type retryReapplier struct {
	calls chan map[string]string
	count int
}

func (r *retryReapplier) Reapply(_ context.Context, overrides map[string]string) error {
	r.count++
	r.calls <- overrides
	if r.count == 1 {
		return errors.New("injected failure")
	}
	return nil
}
func TestFallbackRetriesFailedApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	r := &retryReapplier{calls: make(chan map[string]string, 2)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRelayFallback(ctx, retryStats{}, r, PeerRelayMap{"peer": "relay"}, time.Now().Add(-time.Hour), ticks)
	}()
	defer func() { cancel(); <-done }()
	for i := 0; i < 2; i++ {
		ticks <- time.Now()
		select {
		case got := <-r.calls:
			if got["peer"] != "relay" {
				t.Fatal("missing retry overlay")
			}
		case <-time.After(time.Second):
			t.Fatal("failed fallback not retried")
		}
	}
}
