// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"golang.zx2c4.com/wireguard/wgctrl"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the local tunnel state",
	Long: `Print the current WireGuard interface state: configured peers, last
handshake age, transfer counters. Reads via wgctrl, so it currently
works on Linux (and possibly other wgctrl-supported OSes).`,
	RunE: func(_ *cobra.Command, _ []string) error {
		c, err := wgctrl.New()
		if err != nil {
			return fmt.Errorf("wgctrl: %w", err)
		}
		defer func() { _ = c.Close() }()

		dev, err := c.Device(flagIface)
		if err != nil {
			return fmt.Errorf("device %s: %w", flagIface, err)
		}

		fmt.Printf("interface: %s\n", dev.Name)
		fmt.Printf("  public key: %s\n", dev.PublicKey.String())
		fmt.Printf("  listen port: %d\n", dev.ListenPort)
		fmt.Printf("  peers: %d\n", len(dev.Peers))

		now := time.Now()
		for _, p := range dev.Peers {
			fmt.Printf("\npeer: %s\n", p.PublicKey.String())
			if p.Endpoint != nil {
				fmt.Printf("  endpoint: %s\n", p.Endpoint.String())
			}
			fmt.Printf("  allowed ips: %v\n", p.AllowedIPs)
			if !p.LastHandshakeTime.IsZero() {
				fmt.Printf("  last handshake: %s ago\n", now.Sub(p.LastHandshakeTime).Truncate(time.Second))
			} else {
				fmt.Printf("  last handshake: never\n")
			}
			fmt.Printf("  transfer: %d B received, %d B sent\n", p.ReceiveBytes, p.TransmitBytes)
		}
		return nil
	},
}
