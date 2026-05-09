// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"log/slog"
	"time"

	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
)

// FallbackThreshold is how long bamboo waits for a peer's first
// successful WireGuard handshake before declaring the direct
// endpoint dead and switching that peer to the relay. 60s is
// generous: a healthy LAN handshake completes in ~50ms; even slow
// transcontinental WiFi takes <1s.
const FallbackThreshold = 60 * time.Second

// RelayFallbackInterval is how often the monitor checks per-peer
// stats. 15s strikes a balance: fast enough that fallback feels
// snappy, slow enough that we don't burn cycles polling.
const RelayFallbackInterval = 15 * time.Second

// PeerRelayMap maps a peer's WireGuard public key (base64) to the
// localhost UDP endpoint where the relay client is listening for
// that peer ("127.0.0.1:<port>"). Empty mapping disables fallback.
type PeerRelayMap map[string]string

// StatsProvider is the subset of device.Device the monitor consumes.
// Tests inject a fake.
type StatsProvider interface {
	Stats() ([]device.PeerStats, error)
}

// Reapplier rebuilds the device config given a peer-pubkey ->
// endpoint override map and re-applies it. Tests inject a fake; the
// production implementation lives in clients/cli/cmd/bamboo/up.go.
type Reapplier interface {
	Reapply(ctx context.Context, endpointOverrides map[string]string) error
}

// RunRelayFallback periodically checks per-peer last-handshake time
// against FallbackThreshold. Peers stale longer than that get
// swapped to their relay proxy endpoint via Reapply. Once swapped, a
// peer stays on the relay until the next register / reconnect — we
// do not flap back to direct because a successful relay handshake
// would be the "last_handshake" we'd be checking.
//
// startedAt is the time the device was first configured; we don't
// declare any peer stale before then + FallbackThreshold so the
// initial handshake has time to complete.
func RunRelayFallback(
	ctx context.Context,
	stats StatsProvider,
	reapply Reapplier,
	relays PeerRelayMap,
	startedAt time.Time,
) {
	if len(relays) == 0 {
		return
	}
	t := time.NewTicker(RelayFallbackInterval)
	defer t.Stop()

	overrides := make(map[string]string)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if now.Sub(startedAt) < FallbackThreshold {
				continue
			}
			snapshot, err := stats.Stats()
			if err != nil {
				slog.Debug("relay-fallback stats", "err", err)
				continue
			}
			changed := false
			for _, p := range snapshot {
				relayEP, hasRelay := relays[p.PublicKey]
				if !hasRelay {
					continue
				}
				if _, alreadySwapped := overrides[p.PublicKey]; alreadySwapped {
					continue
				}
				stale := p.LastHandshakeTime.IsZero() ||
					now.Sub(p.LastHandshakeTime) > FallbackThreshold
				if !stale {
					continue
				}
				slog.Info("relay fallback engaged",
					"peer", shortKey(p.PublicKey),
					"last_handshake", p.LastHandshakeTime,
					"direct", p.Endpoint,
					"relay", relayEP,
				)
				overrides[p.PublicKey] = relayEP
				changed = true
			}
			if changed {
				if err := reapply.Reapply(ctx, overrides); err != nil {
					slog.Warn("relay-fallback reapply", "err", err)
				}
			}
		}
	}
}

// ApplyEndpointOverrides returns a copy of cfg with each peer's
// Endpoint replaced when overrides[<base64 pubkey>] is non-empty.
// Production reappliers call this then hand the result to dev.Apply.
func ApplyEndpointOverrides(cfg *wg.DeviceConfig, overrides map[string]string) *wg.DeviceConfig {
	if len(overrides) == 0 || cfg == nil {
		return cfg
	}
	out := *cfg
	out.Peers = make([]wg.PeerConfig, len(cfg.Peers))
	copy(out.Peers, cfg.Peers)
	for i := range out.Peers {
		if ep, ok := overrides[out.Peers[i].PublicKey.Base64()]; ok && ep != "" {
			out.Peers[i].Endpoint = ep
		}
	}
	return &out
}

func shortKey(b64 string) string {
	if len(b64) < 6 {
		return b64
	}
	return b64[:6]
}
