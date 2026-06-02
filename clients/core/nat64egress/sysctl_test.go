// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSysctlPath(t *testing.T) {
	s := sysctlFS{root: "/"}
	if got, want := s.path("net.ipv4.ip_forward"), "/proc/sys/net/ipv4/ip_forward"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestSysctlGetSetRoundTrip(t *testing.T) {
	root := t.TempDir()
	// Pre-create the proc-sys tree for one key, seeded with "0".
	dir := filepath.Join(root, "proc", "sys", "net", "ipv4")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ip_forward"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := sysctlFS{root: root}
	got, err := s.Get("net.ipv4.ip_forward")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0" {
		t.Errorf("Get = %q, want 0 (trimmed)", got)
	}
	if err := s.Set("net.ipv4.ip_forward", "1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("net.ipv4.ip_forward"); got != "1" {
		t.Errorf("after Set, Get = %q, want 1", got)
	}
}
