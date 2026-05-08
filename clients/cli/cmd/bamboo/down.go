// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"

	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Tear down the local WireGuard tunnel",
	Long: `Remove the WireGuard interface created by 'bamboo up'. Useful when
'up' was killed without a graceful shutdown.

Requires Linux + root.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		dev, err := device.New(device.Options{InterfaceName: flagIface})
		if err != nil {
			if errors.Is(err, device.ErrUnsupported) {
				return fmt.Errorf("bamboo down is Linux-only; on macOS use the menu-bar app")
			}
			return fmt.Errorf("device: %w", err)
		}
		if err := dev.Close(); err != nil {
			return fmt.Errorf("close: %w", err)
		}
		fmt.Printf("interface %s removed\n", flagIface)
		return nil
	},
}
