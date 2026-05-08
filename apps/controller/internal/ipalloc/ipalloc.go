// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ipalloc allocates the next free address inside a CIDR given a set
// of already-used addresses.
//
// The current implementation is a simple linear scan; it is O(N) per
// allocation in the size of the address range. That is fine for Phase 1
// where each tenant typically holds a /24 with at most a few hundred peers.
// A future ADR will replace this with a sparse-bitmap or sequence-based
// allocator if required.
package ipalloc

import (
	"errors"
	"fmt"
	"net/netip"
)

// ErrPoolExhausted is returned when no free address can be found.
var ErrPoolExhausted = errors.New("ip pool exhausted")

// NextFree returns the smallest address in poolCIDR that is not already
// in used. The network address (the first address in the CIDR) is reserved
// and never returned.
func NextFree(poolCIDR string, used []string) (string, error) {
	prefix, err := netip.ParsePrefix(poolCIDR)
	if err != nil {
		return "", fmt.Errorf("parse pool: %w", err)
	}

	usedSet := make(map[netip.Addr]struct{}, len(used))
	for _, s := range used {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return "", fmt.Errorf("parse used %q: %w", s, err)
		}
		usedSet[addr] = struct{}{}
	}

	// Start one past the network address.
	candidate := prefix.Masked().Addr().Next()

	for prefix.Contains(candidate) {
		if _, ok := usedSet[candidate]; !ok {
			return candidate.String(), nil
		}
		candidate = candidate.Next()
	}

	return "", ErrPoolExhausted
}
