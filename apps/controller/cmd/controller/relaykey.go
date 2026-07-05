// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"

	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/spf13/cobra"
)

// relaykeyCmd generates an Ed25519 keypair for asymmetric relay-token
// signing (audit C-1 root fix). The private seed goes to the controller
// (BAMBOO_RELAY_SIGNING_KEY); the public key goes to the relay
// (BAMBOO_RELAY_PUBLIC_KEY). With this, the relay holds only the public
// half and a relay-host compromise can't forge tokens.
var relaykeyCmd = &cobra.Command{
	Use:   "relaykey",
	Short: "Generate an Ed25519 keypair for asymmetric relay-token signing",
	Long: "Generate an Ed25519 keypair for relay-token signing.\n\n" +
		"Put the signing key on the CONTROLLER and the public key on the RELAY:\n" +
		"  controller .env:  BAMBOO_RELAY_SIGNING_KEY=<signing key>\n" +
		"  relay .env:       BAMBOO_RELAY_PUBLIC_KEY=<public key>\n\n" +
		"The relay then holds only the public half — a relay-host compromise\n" +
		"cannot forge tokens (audit C-1 root fix).",
	RunE: func(_ *cobra.Command, _ []string) error {
		seed, pub, err := auth.GenerateRelayKeypair()
		if err != nil {
			return err
		}
		fmt.Printf("# Controller (keep secret):\nBAMBOO_RELAY_SIGNING_KEY=%s\n\n", seed)
		fmt.Printf("# Relay (safe to distribute):\nBAMBOO_RELAY_PUBLIC_KEY=%s\n", pub)
		return nil
	},
}
