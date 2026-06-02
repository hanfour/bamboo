// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package nat64egress

import (
	"errors"
	"net/netip"
)

func newManager() Manager { return noopManager{} }

type noopManager struct{}

func (noopManager) Up(_ netip.Prefix, _ netip.Prefix, _ string) error {
	return errors.New("nat64 egress translator is Linux-only")
}
func (noopManager) Down() error { return nil }
