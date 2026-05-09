// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// Shared flags.
var (
	flagAddr     string
	flagTenant   string
	flagAuthKey  string
	flagIface    string
	flagHostname string
	flagLogJSON  bool
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
