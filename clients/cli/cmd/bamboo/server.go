// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// envFlags maps a flag to the environment variable that supplies its
// value when the flag is not given. This lets an installer or a service
// unit configure the client without a long command line, and keeps a
// pre-auth key out of the process list.
var envFlags = map[string]string{
	"server":   "BAMBOO_SERVER",
	"addr":     "BAMBOO_ADDR",
	"tenant":   "BAMBOO_TENANT",
	"auth-key": "BAMBOO_AUTH_KEY",
	"hostname": "BAMBOO_HOSTNAME",
	"iface":    "BAMBOO_IFACE",
}

// applyEnv fills flags that were not set on the command line from their
// environment variable. An explicit flag always wins.
func applyEnv(fs *pflag.FlagSet) error {
	for name, env := range envFlags {
		f := fs.Lookup(name)
		if f == nil || f.Changed {
			continue
		}
		if v := os.Getenv(env); v != "" {
			if err := fs.Set(name, v); err != nil {
				return fmt.Errorf("%s: %w", env, err)
			}
		}
	}
	return nil
}

// target is where the client reaches the controller.
type target struct {
	grpcAddr string // host:port for gRPC
	tls      bool   // dial gRPC over TLS
	httpBase string // base URL for REST calls
}

// resolveTarget turns --server (a URL, the normal case behind a reverse
// proxy) or the legacy --addr (plain host:port for a local controller)
// into the gRPC and REST endpoints. REST falls back to
// BAMBOO_CONTROLLER_HTTP_URL, then to the local development port.
func resolveTarget(server, addr, httpEnv string) (target, error) {
	if server == "" {
		base := httpEnv
		if base == "" {
			base = "http://localhost:8081"
		}
		return target{grpcAddr: addr, httpBase: base}, nil
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return target{}, fmt.Errorf("--server %q: want a URL like https://bamboo.example.com", server)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	base := u.Scheme + "://" + u.Host
	return target{
		grpcAddr: net.JoinHostPort(host, port),
		tls:      u.Scheme == "https",
		httpBase: strings.TrimRight(base, "/"),
	}, nil
}

// activeTarget is set once in PersistentPreRun, after flags and
// environment are applied.
var activeTarget target

func setupTarget(cmd *cobra.Command) error {
	if err := applyEnv(cmd.Flags()); err != nil {
		return err
	}
	t, err := resolveTarget(flagServer, flagAddr, os.Getenv("BAMBOO_CONTROLLER_HTTP_URL"))
	if err != nil {
		return err
	}
	activeTarget = t
	return nil
}
