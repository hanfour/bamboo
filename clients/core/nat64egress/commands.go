// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"context"
	"net/netip"
)

// Filesystem paths the Linux manager owns. ConfigDir holds the rendered
// tayga config; data-dir is set inside RenderTaygaConfig (config.go).
const (
	ConfigDir  = "/etc/tayga"
	ConfigPath = "/etc/tayga/bamboo-nat64.conf"
)

// forwardingKeys are the sysctls the egress must enable to route between
// the NAT64 TUN and the WAN. Their prior values are recorded on Up and
// restored on Down.
var forwardingKeys = []string{"net.ipv4.ip_forward", "net.ipv6.conf.all.forwarding"}

// runner runs an external command and returns its combined output. The
// production implementation is execRun; tests inject a fake. This is the
// repo's first shell-out seam (mirrors detect.go's injectable lookPath).
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// lookPathFn resolves a binary on PATH (exec.LookPath in production).
type lookPathFn func(string) (string, error)

// installCmd splits a PackageManager into the (name, args) to install
// tayga, e.g. {"apt-get", "install", "-y"} -> ("apt-get", ["install","-y","tayga"]).
func installCmd(pm PackageManager) (string, []string) {
	args := append(append([]string{}, pm.InstallCmd[1:]...), "tayga")
	return pm.InstallCmd[0], args
}

// masqueradeArgs builds the iptables argv that source-NATs the v4 dynamic
// pool out of the WAN interface. op is "-C" (check), "-A" (add), or "-D"
// (delete) — check-then-add keeps Up idempotent, -D undoes it on Down.
func masqueradeArgs(op string, v4Pool netip.Prefix, wan string) []string {
	return []string{"-t", "nat", op, "POSTROUTING", "-s", v4Pool.String(), "-o", wan, "-j", "MASQUERADE"}
}

// mktunArgs creates the persistent nat64 TUN from the config.
func mktunArgs(confPath string) []string { return []string{"--config", confPath, "--mktun"} }

// daemonArgs runs tayga in the foreground (so the supervisor owns its
// lifetime) against the config.
func daemonArgs(confPath string) []string { return []string{"--config", confPath, "--nodetach"} }
