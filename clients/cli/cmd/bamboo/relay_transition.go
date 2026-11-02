// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"sync"
)

type relayTransition struct {
	mu      sync.Mutex
	owner   *deviceReapplier
	pending io.Closer
}

func (t *relayTransition) Apply(ctx context.Context, open func() (io.Closer, map[string]string, error)) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending != nil {
		if err := t.owner.restoreCurrent(ctx); err != nil {
			return err
		}
		_ = t.pending.Close()
		t.pending = nil
	}
	candidate, proxies, err := open()
	if err != nil {
		return err
	}
	if err := t.owner.replaceRelay(ctx, proxies); err != nil {
		restoreErr := t.owner.restoreCurrent(ctx)
		if restoreErr == nil {
			_ = candidate.Close()
		} else {
			t.pending = candidate
		}
		return errors.Join(err, restoreErr)
	}
	return nil
}

func (t *relayTransition) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		return nil
	}
	err := t.pending.Close()
	t.pending = nil
	return err
}
