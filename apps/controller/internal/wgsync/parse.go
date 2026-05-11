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
// care about. The wg dump line carries seven more fields (preshared
// key, endpoint, allowed_ips, rx, tx, keepalive); none of them are
// needed for liveness reconciliation.
type PeerState struct {
	PublicKey       string
	LatestHandshake time.Time // zero value means "never"
}

// ParseDump consumes one `wg show <iface> dump` payload. The first
// non-empty line describes the interface (4 fields); subsequent
// lines describe peers (8 fields, tab-separated). The
// latest_handshake column is unix epoch seconds, with 0 meaning
// the peer has not completed a handshake since the interface came
// up.
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
		sec, err := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wg dump line %d: parse latest_handshake %q: %w", lineNum, fields[4], err)
		}
		var ts time.Time
		if sec > 0 {
			ts = time.Unix(sec, 0).UTC()
		}
		peers = append(peers, PeerState{
			PublicKey:       pubkey,
			LatestHandshake: ts,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan wg dump: %w", err)
	}
	return peers, nil
}
