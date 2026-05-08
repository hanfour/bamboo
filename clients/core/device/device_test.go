// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package device_test

import (
	"errors"
	"testing"

	"github.com/hanfour/bamboo/clients/core/device"
)

// On non-Linux hosts, the only assertion we can make is that New refuses
// politely. Linux integration tests need a privileged runner and live in
// a separate, build-tagged file alongside device_linux.go.
func TestNew_UnsupportedOnNonLinux(t *testing.T) {
	_, err := device.New(device.Options{InterfaceName: "bamboo0"})
	if !errors.Is(err, device.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}
