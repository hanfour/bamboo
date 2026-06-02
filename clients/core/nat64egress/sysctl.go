// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"os"
	"path/filepath"
	"strings"
)

// sysctlFS reads and writes kernel sysctls through the /proc/sys
// filesystem. root is "/" in production; tests point it at a temp dir so
// the get/set/restore bookkeeping is verifiable without a real kernel.
type sysctlFS struct{ root string }

// path maps a dotted sysctl key to its /proc/sys file, e.g.
// "net.ipv4.ip_forward" -> "<root>/proc/sys/net/ipv4/ip_forward".
func (s sysctlFS) path(key string) string {
	return filepath.Join(s.root, "proc", "sys", filepath.Join(strings.Split(key, ".")...))
}

// Get returns the current (whitespace-trimmed) value of a sysctl.
func (s sysctlFS) Get(key string) (string, error) {
	b, err := os.ReadFile(s.path(key))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Set writes a sysctl value.
func (s sysctlFS) Set(key, val string) error {
	return os.WriteFile(s.path(key), []byte(val+"\n"), 0o644)
}
