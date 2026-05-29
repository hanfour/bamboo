// SPDX-License-Identifier: AGPL-3.0-or-later

package ipalloc_test

import (
	"errors"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/ipalloc"
)

func TestNextFree_emptyPool(t *testing.T) {
	got, err := ipalloc.NextFree("100.64.0.0/24", nil)
	if err != nil {
		t.Fatalf("NextFree: %v", err)
	}
	if got != "100.64.0.1" {
		t.Errorf("got %q, want 100.64.0.1", got)
	}
}

func TestNextFree_skipsUsed(t *testing.T) {
	got, err := ipalloc.NextFree("100.64.0.0/24",
		[]string{"100.64.0.1", "100.64.0.2", "100.64.0.3"})
	if err != nil {
		t.Fatalf("NextFree: %v", err)
	}
	if got != "100.64.0.4" {
		t.Errorf("got %q, want 100.64.0.4", got)
	}
}

func TestNextFree_findsHole(t *testing.T) {
	got, err := ipalloc.NextFree("100.64.0.0/24",
		[]string{"100.64.0.1", "100.64.0.3"})
	if err != nil {
		t.Fatalf("NextFree: %v", err)
	}
	if got != "100.64.0.2" {
		t.Errorf("got %q, want 100.64.0.2 (filling the hole)", got)
	}
}

func TestNextFree_exhausted(t *testing.T) {
	// /30 has 4 addresses: .0 (network) + .1, .2 (usable) + .3 (broadcast).
	// We give all three non-network addresses as used; expect exhaustion.
	_, err := ipalloc.NextFree("100.64.0.0/30",
		[]string{"100.64.0.1", "100.64.0.2", "100.64.0.3"})
	if !errors.Is(err, ipalloc.ErrPoolExhausted) {
		t.Errorf("err = %v, want ErrPoolExhausted", err)
	}
}

func TestNextFree_invalidPool(t *testing.T) {
	_, err := ipalloc.NextFree("not-a-cidr", nil)
	if err == nil {
		t.Error("expected error for invalid pool")
	}
}

func TestNextFreeDual_pairs(t *testing.T) {
	v4, v6, err := ipalloc.NextFreeDual("100.64.0.0/24", "fdba:1100::/64", nil)
	if err != nil {
		t.Fatalf("NextFreeDual: %v", err)
	}
	if v4 != "100.64.0.1" {
		t.Errorf("v4 = %q, want 100.64.0.1", v4)
	}
	if v6 != "fdba:1100::6440:1" {
		t.Errorf("v6 = %q, want fdba:1100::6440:1", v6)
	}
}

func TestNextFreeDual_v4Exhausted(t *testing.T) {
	_, _, err := ipalloc.NextFreeDual("100.64.0.0/30", "fdba:1100::/64",
		[]string{"100.64.0.1", "100.64.0.2", "100.64.0.3"})
	if !errors.Is(err, ipalloc.ErrPoolExhausted) {
		t.Errorf("err = %v, want ErrPoolExhausted", err)
	}
}
