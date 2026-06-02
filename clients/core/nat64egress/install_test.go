// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnsureTayga_AlreadyInstalled(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "tayga" {
			return "/usr/sbin/tayga", nil
		}
		return "", errors.New("not found")
	}
	called := false
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	if err := EnsureTayga(context.Background(), lookPath, run); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("run should not be called when tayga is already on PATH")
	}
}

func TestEnsureTayga_InstallsViaAptGet(t *testing.T) {
	// tayga absent; apt-get present, others absent.
	lookPath := func(name string) (string, error) {
		if name == "apt-get" {
			return "/usr/bin/apt-get", nil
		}
		return "", errors.New("not found")
	}
	var gotName string
	var gotArgs []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return nil, nil
	}
	if err := EnsureTayga(context.Background(), lookPath, run); err != nil {
		t.Fatal(err)
	}
	if gotName != "apt-get" || strings.Join(gotArgs, " ") != "install -y tayga" {
		t.Errorf("install ran %q %v, want apt-get [install -y tayga]", gotName, gotArgs)
	}
}

func TestEnsureTayga_NoPackageManager(t *testing.T) {
	lookPath := func(string) (string, error) { return "", errors.New("not found") }
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, nil }
	if err := EnsureTayga(context.Background(), lookPath, run); err == nil {
		t.Fatal("want error when neither tayga nor any package manager is present")
	}
}

func TestResolveWANIface_Configured(t *testing.T) {
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		t.Fatal("run should not be called when an iface is configured")
		return nil, nil
	}
	got, err := ResolveWANIface(context.Background(), "eth1", run)
	if err != nil || got != "eth1" {
		t.Errorf("got %q, %v; want eth1, nil", got, err)
	}
}

func TestResolveWANIface_AutoDetect(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ip" || strings.Join(args, " ") != "route get 1.1.1.1" {
			t.Fatalf("unexpected probe: %s %v", name, args)
		}
		return []byte("1.1.1.1 via 10.0.0.1 dev eth0 src 10.0.0.5 uid 0\n"), nil
	}
	got, err := ResolveWANIface(context.Background(), "", run)
	if err != nil || got != "eth0" {
		t.Errorf("got %q, %v; want eth0, nil", got, err)
	}
}
