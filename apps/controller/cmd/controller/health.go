// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Exit 0 when the local HTTP /healthz responds",
	Long: `Probe the controller's own HTTP port. Used as the container
healthcheck: the production image is distroless and has no shell
or wget. BAMBOO_HTTP_ADDR selects the port (default 127.0.0.1:8081);
a wildcard bind address is probed on 127.0.0.1.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		url := healthURL()
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			return fmt.Errorf("GET %s: %w", url, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: status %s", url, resp.Status)
		}
		return nil
	},
}

func healthURL() string {
	addr := os.Getenv("BAMBOO_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8081"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:8081/healthz"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}
