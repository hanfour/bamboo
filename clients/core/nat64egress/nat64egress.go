// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nat64egress manages the host-level NAT64 translator (Tayga) on
// a designated egress peer (NAT64 Phase C2). The real data plane is
// Linux-only; other platforms get a no-op that errors on Up.
package nat64egress

import (
	"net/netip"
	"sync"
)

// Manager owns the NAT64 translator lifecycle on this host. Both methods
// are idempotent — Up converges to "running + routed + NAT'd", Down to
// "nothing of ours present".
type Manager interface {
	Up(prefix netip.Prefix, v4Pool netip.Prefix, wanIface string) error
	Down() error
}

// New returns the platform Manager (the real Tayga manager on Linux, a
// no-op elsewhere).
func New() Manager { return newManager() }

// Reconciler edge-detects the controller-pushed "active" signal and
// drives the Manager. Construct once; call Reconcile on every register
// response. v4Pool + wanIface are host-local config (CLI flags).
type Reconciler struct {
	mgr      Manager
	v4Pool   netip.Prefix
	wanIface string

	mu         sync.Mutex
	lastActive bool
	// applied forces the first Reconcile to act even when active==false
	// (the zero value of lastActive), so a "down" target is realised once.
	applied bool
}

// NewReconciler builds a Reconciler around a Manager + host config.
func NewReconciler(mgr Manager, v4Pool netip.Prefix, wanIface string) *Reconciler {
	return &Reconciler{mgr: mgr, v4Pool: v4Pool, wanIface: wanIface}
}

// Reconcile drives the translator to match active. On a false→true edge
// (or the first call with active=true) it calls Up(prefix, v4Pool,
// wanIface); on true→false it calls Down(). A converged steady state is
// a no-op.
//
// The target is recorded only on SUCCESS: if the Manager errors, the
// reconciler stays unconverged so the NEXT register retries — a
// transient failure (e.g. a busy package manager during install) heals
// on its own instead of stranding the egress until an admin re-toggles
// approval. The caller logs the returned error.
func (r *Reconciler) Reconcile(active bool, prefix netip.Prefix) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.applied && active == r.lastActive {
		return nil
	}
	var err error
	if active {
		err = r.mgr.Up(prefix, r.v4Pool, r.wanIface)
	} else {
		err = r.mgr.Down()
	}
	if err != nil {
		return err // leave unconverged → retry on the next register
	}
	r.lastActive = active
	r.applied = true
	return nil
}
