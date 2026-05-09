// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package device

// New on non-Linux platforms returns ErrUnsupported. macOS bring-up runs
// through the PacketTunnelProvider extension (clients/apple/) instead;
// Windows / BSD support is on the roadmap but not Phase 1.
func New(_ Options) (Device, error) {
	return nil, ErrUnsupported
}
