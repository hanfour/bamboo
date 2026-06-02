// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress_test

import (
	"errors"
	"testing"

	"github.com/hanfour/bamboo/clients/core/nat64egress"
)

func TestDetectPackageManager(t *testing.T) {
	only := func(name string) func(string) (string, error) {
		return func(b string) (string, error) {
			if b == name {
				return "/usr/bin/" + b, nil
			}
			return "", errors.New("not found")
		}
	}
	pm, err := nat64egress.DetectPackageManager(only("dnf"))
	if err != nil || pm.Name != "dnf" {
		t.Errorf("got %+v err=%v, want dnf", pm, err)
	}
	if _, err := nat64egress.DetectPackageManager(func(string) (string, error) { return "", errors.New("none") }); err == nil {
		t.Error("no package manager should error")
	}
}

func TestParseWANIface(t *testing.T) {
	out := "1.1.1.1 via 192.168.1.1 dev eth0 src 192.168.1.50 uid 0"
	got, err := nat64egress.ParseWANIface(out)
	if err != nil || got != "eth0" {
		t.Errorf("got %q err=%v, want eth0", got, err)
	}
	if _, err := nat64egress.ParseWANIface("no device here"); err == nil {
		t.Error("missing dev should error")
	}
}
