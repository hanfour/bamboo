// SPDX-License-Identifier: AGPL-3.0-or-later

package wgsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	setCalls   []setStatusCall
	keptOff    []string // last argument passed to MarkOfflineExcept
	rowsPerKey map[string]int64
	setErr     error
	markErr    error
}

func (f *fakePeerStore) SetStatusByPubKey(_ context.Context, pubKey, status string, lastSeen time.Time) (int64, error) {
	f.setCalls = append(f.setCalls, setStatusCall{pubKey: pubKey, status: status, lastSeen: lastSeen})
	if f.setErr != nil {
		return 0, f.setErr
	}
	if f.rowsPerKey != nil {
		return f.rowsPerKey[pubKey], nil
	}
	return 1, nil
}

func (f *fakePeerStore) MarkOfflineExcept(_ context.Context, pubkeys []string) error {
	cp := append([]string(nil), pubkeys...)
	sort.Strings(cp)
	f.keptOff = cp
	return f.markErr
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

// TestApplyStates_EmptyDumpSkipsZombieSweep documents the safety
// behavior: an empty wg dump (transient interface flap, host script
// hiccup, controller starting before WG is configured) must NOT
// flip every peer offline at once. The reporter still calls
// MarkOfflineExcept with an empty slice; the repo-side guard turns
// that into a no-op (verified in peers_test.go).
func TestApplyStates_EmptyDumpSkipsZombieSweep(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{}
	r := newReporterAt(now, store, 3*time.Minute)

	if err := r.applyStates(context.Background(), nil); err != nil {
		t.Fatalf("applyStates: %v", err)
	}
	if len(store.setCalls) != 0 {
		t.Errorf("setCalls = %d, want 0", len(store.setCalls))
	}
	// keptOff is empty (or nil) — repo treats this as "skip the
	// sweep" so the fake's len-0 record is the right signal.
	if len(store.keptOff) != 0 {
		t.Errorf("keptOff = %v, want empty", store.keptOff)
	}
}

func TestApplyStates_PropagatesSetStatusError(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	want := errors.New("db dead")
	store := &fakePeerStore{setErr: want}
	r := newReporterAt(now, store, 3*time.Minute)

	err := r.applyStates(context.Background(), []PeerState{{PublicKey: "k", LatestHandshake: now}})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrap of %v", err, want)
	}
}

func TestApplyStates_PropagatesMarkOfflineError(t *testing.T) {
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	want := errors.New("sweep failed")
	store := &fakePeerStore{markErr: want}
	r := newReporterAt(now, store, 3*time.Minute)

	err := r.applyStates(context.Background(), []PeerState{{PublicKey: "k", LatestHandshake: now}})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrap of %v", err, want)
	}
}

func TestApplyStates_ZeroRowsAffectedDoesNotError(t *testing.T) {
	// Pubkey is in the wg dump but no DB row matches (e.g. a peer
	// added directly via `wg set` on the host without REST register).
	// Reporter should log debug and continue, not error out.
	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	store := &fakePeerStore{rowsPerKey: map[string]int64{"orphan": 0}}
	r := newReporterAt(now, store, 3*time.Minute)

	err := r.applyStates(context.Background(), []PeerState{{PublicKey: "orphan", LatestHandshake: now}})
	if err != nil {
		t.Errorf("applyStates with 0 rows affected returned %v, want nil", err)
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

// TestReconcile_ReadsAndAppliesFile is the end-to-end happy path:
// write a real wg-show-dump file, call reconcile, verify the fake
// store saw the right calls. Catches breakage in either ParseDump
// or applyStates wiring.
func TestReconcile_ReadsAndAppliesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg-state.txt")
	body := "PRIV\tPUB\t51820\toff\n" +
		"keyA=\t(none)\t1.2.3.4:1\t100.64.0.2/32\t1746876000\t100\t100\t25\n" +
		"keyB=\t(none)\t5.6.7.8:1\t100.64.0.3/32\t0\t0\t0\t0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &fakePeerStore{}
	now := time.Unix(1746876000, 0).UTC().Add(30 * time.Second)
	r := New(Config{Peers: store, StatePath: path, OnlineWindow: 3 * time.Minute})
	r.now = func() time.Time { return now }

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.setCalls) != 2 {
		t.Fatalf("setCalls = %d, want 2", len(store.setCalls))
	}
	if store.setCalls[0].status != "online" {
		t.Errorf("setCalls[0] (recent handshake) status = %s, want online", store.setCalls[0].status)
	}
	if store.setCalls[1].status != "offline" {
		t.Errorf("setCalls[1] (never handshook) status = %s, want offline", store.setCalls[1].status)
	}
}

func TestReconcile_FileMissingReturnsError(t *testing.T) {
	store := &fakePeerStore{}
	r := New(Config{Peers: store, StatePath: "/nonexistent/wg-state.txt"})

	err := r.reconcile(context.Background())
	if err == nil {
		t.Fatal("reconcile: want error for missing file, got nil")
	}
}

func TestReconcile_ParseErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg-state.txt")
	if err := os.WriteFile(path, []byte("bogus\tdata\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &fakePeerStore{}
	r := New(Config{Peers: store, StatePath: path})
	if err := r.reconcile(context.Background()); err == nil {
		t.Fatal("reconcile: want parse error, got nil")
	}
}
