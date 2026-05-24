// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import "time"

// PeerStats describes the runtime state of one WireGuard peer.
// Reported by Device.Stats() so callers can decide whether the peer
// is reachable through its current endpoint or whether to fall back
// to a relay.
type PeerStats struct {
	// PublicKey is the peer's Curve25519 public key, base64-encoded.
	PublicKey string
	// Endpoint is the host:port WireGuard is currently using for this
	// peer. Empty when the peer has not advertised an endpoint.
	Endpoint string
	// LastHandshakeTime is the most recent successful WireGuard
	// handshake. Zero (time.Time{}) means no handshake has happened.
	LastHandshakeTime time.Time
	// RxBytes / TxBytes are cumulative since the device was
	// configured.
	RxBytes uint64
	TxBytes uint64
}

// SumBytes returns the cumulative wg byte counters summed across
// every peer in stats. Powers the §4 P2 bandwidth-reporter the CLI
// daemon forwards on each heartbeat — the controller aggregates
// per-tenant from there, so per-peer detail collapses at the wire.
//
// Returns (TxBytes_total, RxBytes_total) in that order so the
// caller can pass them directly into the controller's
// (bytes_sent, bytes_received) wire fields without re-juggling
// the rx/tx vocabulary mismatch.
func SumBytes(stats []PeerStats) (sent, received uint64) {
	for _, p := range stats {
		sent += p.TxBytes
		received += p.RxBytes
	}
	return
}
