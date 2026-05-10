// SPDX-License-Identifier: AGPL-3.0-or-later

package wgsync

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

type setStatusCall struct {
	pubKey   string
	status   string
	lastSeen time.Time
}

type fakePeerStore struct {
	setCalls []setStatusCall
	keptOff  []string // last argument passed to MarkOfflineExcept
}

func (f *fakePeerStore) SetStatusByPubKey(_ context.Context, pubKey, status string, lastSeen time.Time) error {
	f.setCalls = append(f.setCalls, setStatusCall{pubKey: pubKey, status: status, lastSeen: lastSeen})
	return nil
}

func (f *fakePeerStore) MarkOfflineExcept(_ context.Context, pubkeys []string) error {
	cp := append([]string(nil), pubkeys...)
	sort.Strings(cp)
	f.keptOff = cp
	return nil
}

func newReporterAt(now time.Time, peers PeerStore, win time.Duration) *Reporter {
	r := New(Config{Peers: peers, StatePath: "ignored", OnlineWindow: win})
	r.now = func() time.Time { return now }
	return r
}

func TestApplyStates_OnlineWhenInsideWindow(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{}
	r := newReporterAt(now, store, 3*time.Minute)

	states := []PeerState{
		{PublicKey: "k1", LatestHandshake: now.Add(-30 * time.Second)}, // online
	}
	if err := r.applyStates(context.Background(), states); err != nil {
		t.Fatalf("applyStates: %v", err)
	}
	if len(store.setCalls) != 1 || store.setCalls[0].status != "online" {
		t.Errorf("setCalls = %#v, want one online", store.setCalls)
	}
}

func TestApplyStates_OfflineWhenStaleHandshake(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{}
	r := newReporterAt(now, store, 3*time.Minute)

	states := []PeerState{
		{PublicKey: "stale", LatestHandshake: now.Add(-5 * time.Minute)}, // > window
	}
	if err := r.applyStates(context.Background(), states); err != nil {
		t.Fatalf("applyStates: %v", err)
	}
	if store.setCalls[0].status != "offline" {
		t.Errorf("status = %s, want offline", store.setCalls[0].status)
	}
}

func TestApplyStates_OfflineWhenNeverHandshook(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{}
	r := newReporterAt(now, store, 3*time.Minute)

	states := []PeerState{
		{PublicKey: "fresh", LatestHandshake: time.Time{}}, // zero
	}
	if err := r.applyStates(context.Background(), states); err != nil {
		t.Fatalf("applyStates: %v", err)
	}
	if store.setCalls[0].status != "offline" {
		t.Errorf("never-handshook peer should be offline, got %s", store.setCalls[0].status)
	}
}

func TestApplyStates_BoundaryExactlyAtWindow(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{}
	r := newReporterAt(now, store, 3*time.Minute)

	// At exactly 3min the strict-less-than predicate flips offline.
	states := []PeerState{
		{PublicKey: "edge", LatestHandshake: now.Add(-3 * time.Minute)},
	}
	if err := r.applyStates(context.Background(), states); err != nil {
		t.Fatalf("applyStates: %v", err)
	}
	if store.setCalls[0].status != "offline" {
		t.Errorf("at-boundary peer should be offline, got %s", store.setCalls[0].status)
	}
}

func TestApplyStates_PassesKeepListToMarkOfflineExcept(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{}
	r := newReporterAt(now, store, 3*time.Minute)

	states := []PeerState{
		{PublicKey: "k-a", LatestHandshake: now},
		{PublicKey: "k-b", LatestHandshake: time.Time{}},
	}
	if err := r.applyStates(context.Background(), states); err != nil {
		t.Fatalf("applyStates: %v", err)
	}
	want := []string{"k-a", "k-b"}
	if !reflect.DeepEqual(store.keptOff, want) {
		t.Errorf("keptOff = %v, want %v", store.keptOff, want)
	}
}

func TestApplyStates_EmptyDumpMarksAllOffline(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{}
	r := newReporterAt(now, store, 3*time.Minute)

	if err := r.applyStates(context.Background(), nil); err != nil {
		t.Fatalf("applyStates: %v", err)
	}
	if len(store.setCalls) != 0 {
		t.Errorf("setCalls = %d, want 0", len(store.setCalls))
	}
	if len(store.keptOff) != 0 {
		t.Errorf("keptOff = %v, want empty", store.keptOff)
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	r := New(Config{Peers: &fakePeerStore{}, StatePath: "x"})
	if r.interval != 30*time.Second {
		t.Errorf("default interval = %v, want 30s", r.interval)
	}
	if r.onlineWindow != 3*time.Minute {
		t.Errorf("default onlineWindow = %v, want 3m", r.onlineWindow)
	}
}

func TestRun_NoOpWhenStatePathEmpty(t *testing.T) {
	r := New(Config{Peers: &fakePeerStore{}, StatePath: ""})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Run(ctx); err != nil {
		t.Errorf("Run with empty statePath returned %v, want nil", err)
	}
}
