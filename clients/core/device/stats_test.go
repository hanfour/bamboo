// SPDX-License-Identifier: AGPL-3.0-or-later

package device_test

import (
	"testing"

	"github.com/hanfour/bamboo/clients/core/device"
)

// TestSumBytes pins the §4 P2 bandwidth-reporter aggregation
// contract: SumBytes returns (tx_total, rx_total) summed across
// every peer. Order matters because the caller forwards directly
// into the controller's (bytes_sent, bytes_received) wire pair —
// flipping them would silently report send as receive on the
// dashboard.
func TestSumBytes(t *testing.T) {
	cases := []struct {
		name               string
		stats              []device.PeerStats
		wantSent, wantRecv uint64
	}{
		{
			name:     "empty slice",
			stats:    nil,
			wantSent: 0, wantRecv: 0,
		},
		{
			name: "single peer",
			stats: []device.PeerStats{
				{TxBytes: 100, RxBytes: 50},
			},
			wantSent: 100, wantRecv: 50,
		},
		{
			name: "three peers sums in order",
			stats: []device.PeerStats{
				{TxBytes: 1000, RxBytes: 100},
				{TxBytes: 2000, RxBytes: 200},
				{TxBytes: 4000, RxBytes: 400},
			},
			wantSent: 7000, wantRecv: 700,
		},
		{
			name: "tx-only peer (one-way traffic) still counted",
			stats: []device.PeerStats{
				{TxBytes: 999, RxBytes: 0},
				{TxBytes: 0, RxBytes: 111},
			},
			wantSent: 999, wantRecv: 111,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSent, gotRecv := device.SumBytes(tc.stats)
			if gotSent != tc.wantSent || gotRecv != tc.wantRecv {
				t.Errorf("SumBytes = (%d, %d), want (%d, %d)",
					gotSent, gotRecv, tc.wantSent, tc.wantRecv)
			}
		})
	}
}
