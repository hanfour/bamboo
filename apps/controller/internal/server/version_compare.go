// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "golang.org/x/mod/semver"

// upgradeAvailable returns true iff `peerVersion` is strictly behind
// `latest` per semver. Empty either side → false (we don't know,
// don't badge). Malformed either side → false (defensive: a v-less
// dev string must not produce a false positive that nags the
// operator every refresh).
//
// `golang.org/x/mod/semver` expects a leading "v"; normalizeSemver
// adds it so callers can pass either "0.1.4" or "v0.1.4" without
// thinking about which.
func upgradeAvailable(peerVersion, latest string) bool {
	if peerVersion == "" || latest == "" {
		return false
	}
	pv := normalizeSemver(peerVersion)
	lv := normalizeSemver(latest)
	if !semver.IsValid(pv) || !semver.IsValid(lv) {
		return false
	}
	return semver.Compare(pv, lv) < 0
}

func normalizeSemver(s string) string {
	if s == "" || s[0] == 'v' {
		return s
	}
	return "v" + s
}
