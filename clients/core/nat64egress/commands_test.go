// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestInstallCmd(t *testing.T) {
	pm := PackageManager{Name: "apt-get", InstallCmd: []string{"apt-get", "install", "-y"}}
	name, args := installCmd(pm)
	if name != "apt-get" {
		t.Errorf("name = %q, want apt-get", name)
	}
	if want := []string{"install", "-y", "tayga"}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestMasqueradeArgs(t *testing.T) {
	pool := netip.MustParsePrefix("192.168.255.0/24")
	got := masqueradeArgs("-A", pool, "eth0")
	want := []string{"-t", "nat", "-A", "POSTROUTING", "-s", "192.168.255.0/24", "-o", "eth0", "-j", "MASQUERADE"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("masqueradeArgs(-A) = %v, want %v", got, want)
	}
	if op := masqueradeArgs("-C", pool, "eth0")[2]; op != "-C" {
		t.Errorf("check op not threaded: got %q", op)
	}
}

func TestTaygaArgs(t *testing.T) {
	if got, want := mktunArgs("/etc/tayga/bamboo-nat64.conf"),
		[]string{"--config", "/etc/tayga/bamboo-nat64.conf", "--mktun"}; !reflect.DeepEqual(got, want) {
		t.Errorf("mktunArgs = %v, want %v", got, want)
	}
	if got, want := daemonArgs("/etc/tayga/bamboo-nat64.conf"),
		[]string{"--config", "/etc/tayga/bamboo-nat64.conf", "--nodetach"}; !reflect.DeepEqual(got, want) {
		t.Errorf("daemonArgs = %v, want %v", got, want)
	}
}

func TestConstants(t *testing.T) {
	if ConfigPath != "/etc/tayga/bamboo-nat64.conf" {
		t.Errorf("ConfigPath = %q", ConfigPath)
	}
	if len(forwardingKeys) != 2 {
		t.Errorf("forwardingKeys = %v, want 2 entries", forwardingKeys)
	}
}
