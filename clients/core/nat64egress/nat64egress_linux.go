// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package nat64egress

import (
	"errors"
	"net/netip"
)

// linuxManager is the real Tayga lifecycle manager. PR 1 ships a stub so
// the daemon compiles + the control-plane wiring is testable; PR 2
// implements Up/Down (install, TUN, routes, sysctl, MASQUERADE, process).
func newManager() Manager { return &linuxManager{} }

type linuxManager struct{}

func (*linuxManager) Up(_ netip.Prefix, _ netip.Prefix, _ string) error {
	return errors.New("nat64 egress: Tayga manager not implemented yet (NAT64 Phase C2 PR 2)")
}
func (*linuxManager) Down() error { return nil }
