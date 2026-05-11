// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wgsync mirrors host WireGuard liveness into the peers
// table so the UI's "online / last seen" columns reflect actual
// cryptokey-routing state rather than the once-set-and-stuck
// status that Heartbeat-only tracking produces.
//
// The controller container has no access to the host's WireGuard
// netlink. A host-side systemd timer writes `wg show $IFACE dump`
// to a file; this package parses that file on a ticker and
// reconciles each peer's row.
package wgsync

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// PeerState is the per-peer subset of `wg show <iface> dump` we
// care about. The wg dump line carries one more field (preshared
// key at index 1) and one (keepalive at index 7) we ignore.
//
// Field semantics mirror the wg dump format directly so the parser
// can stay a near-1:1 translation:
//   - Endpoint is "" when wg dump prints "(none)" (peer has never
//     handshook from a known host:port).
//   - RxBytes / TxBytes are cumulative counters since the peer was
//     added to the interface; they reset to 0 on interface restart
//     and are always present (even pre-handshake they read 0).
type PeerState struct {
	PublicKey       string
	Endpoint        string    // "" when wg dump shows "(none)"
	LatestHandshake time.Time // zero value means "never"
	RxBytes         int64
	TxBytes         int64
}

// ParseDump consumes one `wg show <iface> dump` payload. The first
// non-empty line describes the interface (4 fields); subsequent
// lines describe peers (8 fields, tab-separated):
//
//	[0] public_key
//	[1] preshared_key (always "(none)" for us)
//	[2] endpoint        — "host:port" or "(none)"
//	[3] allowed_ips
//	[4] latest_handshake — unix epoch seconds; 0 = never handshook
//	[5] rx_bytes
//	[6] tx_bytes
//	[7] persistent_keepalive
func ParseDump(r io.Reader) ([]PeerState, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<16), 1<<20)

	var peers []PeerState
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		// First non-empty line is the interface row (4 fields).
		// Don't gate on lineNum because callers sometimes feed a
		// stripped dump; gate on field count instead.
		if len(fields) == 4 {
			continue
		}
		if len(fields) != 8 {
			return nil, fmt.Errorf("wg dump line %d: expected 4 or 8 tab-separated fields, got %d", lineNum, len(fields))
		}
		pubkey := strings.TrimSpace(fields[0])
		if pubkey == "" {
			return nil, fmt.Errorf("wg dump line %d: empty public key", lineNum)
		}
		endpoint := strings.TrimSpace(fields[2])
		if endpoint == "(none)" {
			endpoint = ""
		}
		sec, err := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wg dump line %d: parse latest_handshake %q: %w", lineNum, fields[4], err)
		}
		var ts time.Time
		if sec > 0 {
			ts = time.Unix(sec, 0).UTC()
		}
		rx, err := strconv.ParseInt(strings.TrimSpace(fields[5]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wg dump line %d: parse rx_bytes %q: %w", lineNum, fields[5], err)
		}
		tx, err := strconv.ParseInt(strings.TrimSpace(fields[6]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wg dump line %d: parse tx_bytes %q: %w", lineNum, fields[6], err)
		}
		peers = append(peers, PeerState{
			PublicKey:       pubkey,
			Endpoint:        endpoint,
			LatestHandshake: ts,
			RxBytes:         rx,
			TxBytes:         tx,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan wg dump: %w", err)
	}
	return peers, nil
}
