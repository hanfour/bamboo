// SPDX-License-Identifier: AGPL-3.0-or-later

package wgsync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// Statuses written by the reporter. Match the values the rest of
// the controller (and the Web UI) already use; see
// apps/controller/internal/db/repo/peers.go and
// apps/web/src/lib/types.ts.
const (
	statusOnline  = "online"
	statusOffline = "offline"
)

// PeerStore is the repo subset the reporter calls. Defined as an
// interface so reporter_test.go can drive it without a database;
// *repo.Peers satisfies it in production.
//
// SyncWGState returns the number of rows updated; the reporter
// uses 0 to detect "pubkey in wg dump but not in DB" — typically a
// peer manually `wg set`d on the host that never registered via REST.
type PeerStore interface {
	SyncWGState(ctx context.Context, state repo.WGSyncState) (int64, error)
	MarkOfflineExcept(ctx context.Context, pubkeys []string) error
}

// Reporter reconciles peer liveness from a wg-show-dump file.
//
// File-based input is deliberate: the controller runs in a docker
// container that cannot reach the host's wireguard netlink, and
// adding host-network mode would force the rest of the compose
// stack onto host networking too. A host systemd timer writes the
// dump; this reporter reads it.
type Reporter struct {
	peers        PeerStore
	statePath    string
	interval     time.Duration
	onlineWindow time.Duration
	now          func() time.Time
}

// Config bundles reporter dependencies. Zero-valued time fields
// fall back to production defaults.
type Config struct {
	Peers        PeerStore
	StatePath    string
	Interval     time.Duration // default 30s
	OnlineWindow time.Duration // default 3m
}

// New constructs a Reporter. An empty StatePath disables the
// reporter; Run becomes a no-op so callers don't have to special-
// case dev / non-VPS deploys.
func New(cfg Config) *Reporter {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	win := cfg.OnlineWindow
	if win <= 0 {
		win = 3 * time.Minute
	}
	return &Reporter{
		peers:        cfg.Peers,
		statePath:    cfg.StatePath,
		interval:     interval,
		onlineWindow: win,
		now:          time.Now,
	}
}

// Run blocks until ctx is canceled. Each tick reads the wg dump
// file and reconciles every peer's status into the DB. Reconcile
// errors are logged at warn and don't stop the ticker — a missing
// or stale dump file is preferable to crashing the controller.
func (r *Reporter) Run(ctx context.Context) error {
	if r.statePath == "" {
		slog.Info("wgsync reporter disabled (no state path configured)")
		return nil
	}
	slog.Info("wgsync reporter starting",
		"state_path", r.statePath,
		"interval", r.interval,
		"online_window", r.onlineWindow,
	)

	// Tick immediately so the UI reflects fresh state on startup
	// instead of waiting one full interval.
	if err := r.reconcile(ctx); err != nil {
		slog.Warn("wgsync initial reconcile failed", "err", err)
	}

	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := r.reconcile(ctx); err != nil {
				slog.Warn("wgsync reconcile failed", "err", err)
			}
		}
	}
}

func (r *Reporter) reconcile(ctx context.Context) error {
	// statePath is operator-supplied via env / yaml; intentional read.
	f, err := os.Open(r.statePath) //nolint:gosec // G304: trusted operator input
	if err != nil {
		return fmt.Errorf("open %s: %w", r.statePath, err)
	}
	defer func() { _ = f.Close() }()

	states, err := ParseDump(f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", r.statePath, err)
	}
	return r.applyStates(ctx, states)
}

// applyStates is the pure reconcile step factored out so tests can
// drive it without touching the filesystem.
func (r *Reporter) applyStates(ctx context.Context, states []PeerState) error {
	now := r.now()
	pubkeys := make([]string, 0, len(states))
	for _, s := range states {
		pubkeys = append(pubkeys, s.PublicKey)
		status := statusOffline
		if !s.LatestHandshake.IsZero() && now.Sub(s.LatestHandshake) < r.onlineWindow {
			status = statusOnline
		}
		n, err := r.peers.SyncWGState(ctx, repo.WGSyncState{
			PubKey:        s.PublicKey,
			Status:        status,
			LastHandshake: s.LatestHandshake,
			Endpoint:      s.Endpoint,
			RxBytes:       s.RxBytes,
			TxBytes:       s.TxBytes,
		})
		if err != nil {
			return fmt.Errorf("sync wg state pubkey=%s: %w", s.PublicKey, err)
		}
		if n == 0 {
			// Pubkey is in the wg dump but no DB row matches.
			// Usually means a peer was added directly with
			// `wg set ... peer ...` on the host without going
			// through the REST register flow. Harmless; log so
			// operators can spot drift.
			slog.Debug("wgsync: pubkey in wg dump has no peers row", "pubkey", s.PublicKey)
		}
	}
	// Peers in the DB whose pubkey is no longer in the wg dump are
	// zombies (e.g. removed via `wg set ... peer ... remove`). Mark
	// them offline rather than deleting the row, per the design
	// decision to keep history. The repo treats an empty pubkeys
	// list as "skip the sweep" so a transient empty dump doesn't
	// flip every peer offline at once.
	if err := r.peers.MarkOfflineExcept(ctx, pubkeys); err != nil {
		return fmt.Errorf("mark zombies offline: %w", err)
	}
	return nil
}
