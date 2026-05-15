// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// peerDNSResolver is the slice of repo.Peers the resolver needs.
// Defined as an interface here so SlugifyHostname / ResolveDNSName can
// be unit-tested without standing up a real database.
type peerDNSResolver interface {
	IsDNSNameTaken(ctx context.Context, tenantID uuid.UUID, name string) (bool, error)
}

// maxDNSLabelLen is the RFC 1035 per-label limit. A bamboo dns-name
// like "mac-mini.bamboo" only consumes <label>; the ".bamboo" suffix
// is added at resolution time, never stored in this column.
const maxDNSLabelLen = 63

// SlugifyHostname turns a freeform hostname into a DNS-safe label.
// The output is lowercase, conforms to `[a-z0-9-]+`, has no leading /
// trailing dash, no consecutive dashes, and is at most 63 characters.
// Returns the empty string when the input has no convertible
// characters (e.g. all-emoji); the caller is responsible for choosing
// a fallback (typically "peer").
//
// The function is deliberately strict — it preserves only the
// characters that DNS resolvers across the ecosystem accept. Non-
// ASCII hostnames (e.g. "黃柏漢的 Mac mini") therefore collapse to
// empty, and we fall back to a synthetic base. Future revisions can
// add IDN/Punycode if we ever need true Unicode dns-names.
func SlugifyHostname(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case b.Len() > 0 && !lastDash:
			// One dash between runs of alphanumerics. Skips when
			// b is empty (no leading dash) or when the previous
			// emitted char was already a dash (no doubles).
			b.WriteRune('-')
			lastDash = true
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if len(out) > maxDNSLabelLen {
		out = strings.TrimSuffix(out[:maxDNSLabelLen], "-")
	}
	return out
}

// ResolveDNSName returns a peer_dns_name that is unique within the
// tenant — base if available, otherwise base-2, base-3, ... up to a
// reasonable ceiling. The candidate is truncated as needed so the
// final string respects the 63-char DNS-label limit even after the
// numeric suffix is appended.
//
// When base is empty (Slugify produced nothing usable) the function
// substitutes "peer" so the resolver always has a non-empty seed.
//
// The 100-attempt ceiling is theatre — in practice the suffix range
// is enormous because each new tenant peer hits a fresh starting
// point. The bound exists so a misconfigured caller (e.g. all peers
// reporting the literal hostname "x") fails loudly instead of
// looping forever.
func ResolveDNSName(ctx context.Context, repo peerDNSResolver, tenantID uuid.UUID, base string) (string, error) {
	if base == "" {
		base = "peer"
	}
	for i := 0; i < 100; i++ {
		candidate := candidateWithSuffix(base, i)
		taken, err := repo.IsDNSNameTaken(ctx, tenantID, candidate)
		if err != nil {
			return "", fmt.Errorf("dns name collision check: %w", err)
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("dns name space exhausted for base %q", base)
}

// candidateWithSuffix builds the i-th candidate string for base.
// i=0 returns base verbatim; i>0 returns "<truncated-base>-<i+1>".
// Truncation guarantees the result fits within maxDNSLabelLen.
func candidateWithSuffix(base string, i int) string {
	if i == 0 {
		return base
	}
	suffix := fmt.Sprintf("-%d", i+1)
	maxBase := maxDNSLabelLen - len(suffix)
	trimmed := base
	if len(trimmed) > maxBase {
		trimmed = strings.TrimSuffix(trimmed[:maxBase], "-")
	}
	return trimmed + suffix
}
