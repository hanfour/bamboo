// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "controller",
	Short: "bamboo control plane server",
	Long: `bamboo control plane: authentication, peer coordination,
ACL evaluation, and audit log.`,
}

func Execute() {
	cobra.CheckErr(rootCmd.Execute())
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(versionCmd)
}
