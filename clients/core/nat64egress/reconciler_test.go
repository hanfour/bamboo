// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/hanfour/bamboo/clients/core/nat64egress"
)

type fakeManager struct{ ups, downs int }

func (f *fakeManager) Up(_ netip.Prefix, _ netip.Prefix, _ string) error { f.ups++; return nil }
func (f *fakeManager) Down() error                                       { f.downs++; return nil }

func TestReconciler_EdgesOnly(t *testing.T) {
	f := &fakeManager{}
	r := nat64egress.NewReconciler(f, netip.MustParsePrefix("192.168.255.0/24"), "eth0")
	p := netip.MustParsePrefix("64:ff9b::/96")

	r.Reconcile(true, p)
	r.Reconcile(true, p)
	r.Reconcile(false, p)
	r.Reconcile(false, p)
	r.Reconcile(true, p)

	if f.ups != 2 || f.downs != 1 {
		t.Errorf("ups=%d downs=%d, want 2/1", f.ups, f.downs)
	}
}

func TestReconciler_FirstFalseDrivesDown(t *testing.T) {
	f := &fakeManager{}
	r := nat64egress.NewReconciler(f, netip.MustParsePrefix("192.168.255.0/24"), "")
	r.Reconcile(false, netip.MustParsePrefix("64:ff9b::/96"))
	if f.downs != 1 {
		t.Errorf("first reconcile(false) should drive Down once, got %d", f.downs)
	}
}

// errManager errors on the first n Up calls, then succeeds.
type errManager struct {
	failUps int
	ups     int
}

func (e *errManager) Up(_ netip.Prefix, _ netip.Prefix, _ string) error {
	e.ups++
	if e.ups <= e.failUps {
		return errTest
	}
	return nil
}
func (e *errManager) Down() error { return nil }

var errTest = errors.New("boom")

func TestReconciler_RetriesAfterError(t *testing.T) {
	e := &errManager{failUps: 1} // first Up fails, second succeeds
	r := nat64egress.NewReconciler(e, netip.MustParsePrefix("192.168.255.0/24"), "")
	p := netip.MustParsePrefix("64:ff9b::/96")

	if err := r.Reconcile(true, p); err == nil {
		t.Fatal("first Reconcile(true) should surface the Up error")
	}
	// Error left it unconverged → a second active reconcile retries Up.
	if err := r.Reconcile(true, p); err != nil {
		t.Fatalf("retry Reconcile(true) should succeed, got %v", err)
	}
	if e.ups != 2 {
		t.Errorf("Up called %d times, want 2 (1 fail + 1 retry)", e.ups)
	}
	// Now converged: a third steady call is a no-op.
	_ = r.Reconcile(true, p)
	if e.ups != 2 {
		t.Errorf("steady-state reconcile should not re-Up; ups=%d", e.ups)
	}
}
