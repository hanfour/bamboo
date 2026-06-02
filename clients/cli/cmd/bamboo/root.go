// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log/slog"
	"os"

	"github.com/hanfour/bamboo/clients/core/nat64egress"
	"github.com/spf13/cobra"
)

// Shared flags.
var (
	flagAddr                 string
	flagTenant               string
	flagAuthKey              string
	flagIface                string
	flagHostname             string
	flagLogJSON              bool
	flagWGListenPort         uint16
	flagAdvertiseRoutes      []string
	flagAdvertiseExitNode    bool
	flagAdvertiseNAT64Egress bool
	flagNAT64V4Pool          string
	flagNAT64WANIface        string
)

var rootCmd = &cobra.Command{
	Use:   "bamboo",
	Short: "bamboo mesh-VPN client",
	Long: `bamboo is the command-line client for the bamboo mesh VPN.

Bringing up an interface (bamboo up) requires root / CAP_NET_ADMIN on
Linux and is currently unsupported on other platforms; macOS users
should use the app under clients/apple/.`,
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		configureLogger(flagLogJSON)
	},
	// Don't dump the entire usage block on every RunE error; the error
	// message is enough.
	SilenceUsage: true,
}

// Execute runs the root command.
func Execute() {
	cobra.CheckErr(rootCmd.Execute())
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagAddr, "addr", "localhost:8080", "controller gRPC address")
	rootCmd.PersistentFlags().StringVar(&flagTenant, "tenant", "default", "tenant slug; ignored if --auth-key is set")
	rootCmd.PersistentFlags().StringVar(&flagAuthKey, "auth-key", "", "pre-auth key secret (bka_..._...)")
	rootCmd.PersistentFlags().StringVar(&flagIface, "iface", "bamboo0", "WireGuard interface name")
	rootCmd.PersistentFlags().StringVar(&flagHostname, "hostname", defaultHostname(), "peer hostname to register")
	rootCmd.PersistentFlags().BoolVar(&flagLogJSON, "log-json", false, "emit JSON-formatted logs (default: text)")
	rootCmd.PersistentFlags().Uint16Var(&flagWGListenPort, "wg-listen-port", 0, "WireGuard UDP listen port; 0 = pick a free port (the same port is used for STUN discovery so the advertised endpoint matches what other peers can actually dial)")
	rootCmd.PersistentFlags().StringSliceVar(&flagAdvertiseRoutes, "advertise-routes", nil, "advertise these CIDRs as reachable through this peer (subnet router; issue #136); admin must approve before they merge into other peers' allowed_ips. Comma-separated or repeated, e.g. --advertise-routes 10.0.0.0/24,192.168.86.0/24")
	rootCmd.PersistentFlags().BoolVar(&flagAdvertiseExitNode, "advertise-exit-node", false, "advertise this peer as an exit-node candidate (issue #137); admin must still approve via POST /api/v1/peers/{id}/exit-node before any peer routes its default through here")
	rootCmd.PersistentFlags().BoolVar(&flagAdvertiseNAT64Egress, "advertise-nat64-egress", false, "advertise this peer as a NAT64 translator-egress candidate (NAT64 Phase C); admin must still approve via POST /api/v1/peers/{id}/nat64-egress, and the box must actually run a translator (Phase C2) for traffic to flow")
	rootCmd.PersistentFlags().StringVar(&flagNAT64V4Pool, "nat64-v4-pool", nat64egress.DefaultV4Pool, "NAT64 egress: Tayga's v4 dynamic pool (NAT64 Phase C2); override if it collides with this box's networks")
	rootCmd.PersistentFlags().StringVar(&flagNAT64WANIface, "nat64-wan-iface", "", "NAT64 egress: uplink interface for MASQUERADE (NAT64 Phase C2); empty = auto-detect the default route")

	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(downCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(versionCmd)
}

func configureLogger(jsonOutput bool) {
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if jsonOutput {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func defaultHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "bamboo-peer"
	}
	return h
}
