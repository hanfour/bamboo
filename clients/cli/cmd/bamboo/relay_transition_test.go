// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/wg"
)

type transitionCloser struct{ closed bool }

func (c *transitionCloser) Close() error { c.closed = true; return nil }

type transitionDevice struct {
	compositionDevice
	failures int
	before   func()
}

func (d *transitionDevice) Apply(ctx context.Context, cfg *wg.DeviceConfig) error {
	if d.before != nil {
		d.before()
	}
	_ = d.compositionDevice.Apply(ctx, cfg)
	if d.failures > 0 {
		d.failures--
		return errors.New("post-write failure")
	}
	return nil
}

func TestRelayTransitionRollbackBeforeClose(t *testing.T) {
	ctx := context.Background()
	key := wg.PublicKey{1}
	d := &transitionDevice{}
	r := &deviceReapplier{dev: d}
	if err := r.Apply(ctx, &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key, Endpoint: "direct"}}}); err != nil {
		t.Fatal(err)
	}
	c := &transitionCloser{}
	d.failures = 1
	d.before = func() {
		if c.closed {
			t.Fatal("closed before restore")
		}
	}
	tr := &relayTransition{owner: r}
	err := tr.Apply(ctx, func() (io.Closer, map[string]string, error) { return c, map[string]string{key.Base64(): "new"}, nil })
	if err == nil || !c.closed || d.cfg.Peers[0].Endpoint != "direct" {
		t.Fatal("failed transition did not restore before close")
	}
}

func TestRelayTransitionRetainsPendingAndLatestAuthority(t *testing.T) {
	ctx := context.Background()
	key := wg.PublicKey{1}
	d := &transitionDevice{}
	r := &deviceReapplier{dev: d}
	if err := r.Apply(ctx, &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key}}}); err != nil {
		t.Fatal(err)
	}
	tr := &relayTransition{owner: r}
	old, active := &transitionCloser{}, &transitionCloser{}
	calls := 0
	open := func() (io.Closer, map[string]string, error) {
		calls++
		return old, map[string]string{key.Base64(): "old"}, nil
	}
	d.failures = 3
	if err := tr.Apply(ctx, open); err == nil || old.closed {
		t.Fatal("candidate not retained")
	}
	if err := tr.Apply(ctx, open); err == nil || calls != 1 {
		t.Fatal("opened while restoration failed")
	}
	if err := r.Apply(ctx, &wg.DeviceConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Apply(ctx, func() (io.Closer, map[string]string, error) {
		if !old.closed || len(d.cfg.Peers) != 0 {
			t.Fatal("pending restore used stale authority")
		}
		return active, map[string]string{key.Base64(): "new"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if active.closed || len(d.cfg.Peers) != 0 {
		t.Fatal("active ownership or latest authority lost")
	}
}

func TestRelayTransitionClosePending(t *testing.T) {
	d := &transitionDevice{failures: 2}
	r := &deviceReapplier{dev: d, base: &wg.DeviceConfig{}}
	tr := &relayTransition{owner: r}
	c := &transitionCloser{}
	if err := tr.Apply(context.Background(), func() (io.Closer, map[string]string, error) { return c, nil, nil }); err == nil {
		t.Fatal("expected error")
	}
	if err := tr.Close(); err != nil || !c.closed {
		t.Fatal("pending not closed")
	}
}

func TestRelayTransitionSerializesOpenAndApply(t *testing.T) {
	tr := &relayTransition{owner: &deviceReapplier{dev: &compositionDevice{}, base: &wg.DeviceConfig{}}}
	entered, release, secondOpened := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	var opened atomic.Int32
	go func() {
		done <- tr.Apply(context.Background(), func() (io.Closer, map[string]string, error) {
			opened.Add(1)
			close(entered)
			<-release
			return &transitionCloser{}, nil, nil
		})
	}()
	<-entered
	go func() {
		done <- tr.Apply(context.Background(), func() (io.Closer, map[string]string, error) {
			opened.Add(1)
			close(secondOpened)
			return &transitionCloser{}, nil, nil
		})
	}()
	early := false
	select {
	case <-secondOpened:
		early = true
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if opened.Load() != 2 {
		t.Fatal("second opener did not run")
	}
	if early {
		t.Fatal("second opener ran during an unfinished transition")
	}
}

func TestInitialRelayWorkerCapturesGenerationBeforeSwap(t *testing.T) {
	ctx := context.Background()
	key := wg.PublicKey{1}
	d := &compositionDevice{}
	r := &deviceReapplier{dev: d, base: &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: key}}}}
	initial := r.bound()
	release, done := make(chan struct{}), make(chan error, 1)
	go func() { <-release; done <- initial.Reapply(ctx, map[string]string{key.Base64(): "old"}) }()
	if err := r.replaceRelay(ctx, map[string]string{key.Base64(): "new"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d.cfg.Peers[0].Endpoint != "new" {
		t.Fatal("initial worker used new generation")
	}
}
