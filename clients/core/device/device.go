// SPDX-License-Identifier: AGPL-3.0-or-later

// Package device drives the local WireGuard interface.
//
// The Device interface is platform-agnostic. The Linux implementation
// (device_linux.go) uses wgctrl + netlink and requires root /
// CAP_NET_ADMIN. Other platforms get a stub that errors out;
// macOS / iOS bring-up flows through the PacketTunnelProvider instead
// (clients/apple/PacketTunnelProvider-{macOS,iOS}/).
package device

import (
	"context"
	"errors"

	"github.com/hanfour/bamboo/clients/core/wg"
)

// ErrUnsupported is returned by New on platforms that do not have a
// native bring-up path. CLI users on those platforms should use the
// platform's app target (e.g. clients/apple/) instead.
var ErrUnsupported = errors.New("device bring-up not supported on this OS in CLI mode")

// Device is the platform-specific handle to a configured WireGuard interface.
//
// Apply is idempotent: calling it again with a new config replaces the
// interface state. Close removes the interface.
type Device interface {
	// Apply (re)configures the interface from cfg: assigns Address,
	// installs the WireGuard private/peer set, brings the link up, and
	// installs routes for each peer's AllowedIPs.
	Apply(ctx context.Context, cfg *wg.DeviceConfig) error
	// Close removes the interface. Subsequent Apply calls fail.
	Close() error
}

// Options is the construction-time configuration for a Device.
type Options struct {
	// InterfaceName is the OS-level link name (e.g. "bamboo0"). Must be
	// short enough for the platform: 15 chars on Linux.
	InterfaceName string
	// MTU defaults to 1420 if zero (a safe default for WireGuard over IPv4).
	MTU int
}

// New is implemented per-platform. See device_linux.go and device_other.go.
//
// Production callers must check err for ErrUnsupported and surface a
// helpful message.
