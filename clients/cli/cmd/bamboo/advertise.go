// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/netip"
)

// validateAdvertisedRoutes parses each entry as a CIDR prefix and
// returns the first parse failure along with the offending input.
// Called once at `bamboo up` startup so a typo (e.g.
// "10.0.0.0\24" instead of "10.0.0.0/24") fails immediately with a
// clear local error rather than after a controller-side 400 round
// trip — the controller validates the same way at
// apps/controller/internal/server/api.go:1163 but the request gets
// further along (auth-key + tenant lookup) before failing, which
// muddles the error in user-visible logs.
//
// Empty slice is fine (no advertise); the caller treats nil/empty
// the same.
func validateAdvertisedRoutes(routes []string) error {
	for _, r := range routes {
		if _, err := netip.ParsePrefix(r); err != nil {
			return fmt.Errorf("advertise-routes: %q is not a valid CIDR: %w", r, err)
		}
	}
	return nil
}
