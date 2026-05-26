// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "testing"

// TestUpgradeAvailable pins the semver-compare contract used by the
// peers handler to surface the upgrade indicator. Each case is
// deliberately a single (peer, latest, want) triplet so a regression
// names the exact failing path.
func TestUpgradeAvailable(t *testing.T) {
	cases := []struct {
		name   string
		peer   string
		latest string
		want   bool
	}{
		{"behind", "0.1.3", "0.1.4", true},
		{"equal", "0.1.4", "0.1.4", false},
		{"ahead", "0.1.5", "0.1.4", false},
		{"empty peer", "", "0.1.4", false},
		{"empty latest", "0.1.4", "", false},
		{"malformed peer", "dev", "0.1.4", false},
		{"v-prefixed peer", "v0.1.3", "0.1.4", true},
		{"pre-release behind stable", "0.1.4-rc1", "0.1.4", true},
		{"malformed latest", "0.1.4", "nightly", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := upgradeAvailable(tc.peer, tc.latest)
			if got != tc.want {
				t.Errorf("upgradeAvailable(%q, %q) = %v, want %v",
					tc.peer, tc.latest, got, tc.want)
			}
		})
	}
}
