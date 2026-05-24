// SPDX-License-Identifier: Apache-2.0

// support_bundle.go drives the `bamboo support-bundle` subcommand
// (§4 P2 operations). When an operator opens a support ticket the
// hardest part for the bamboo team is reconstructing what was
// running on the client: which controller, which interface state,
// which version, which env. This command bundles all of that into
// one ZIP they can attach.
//
// Layout of the bundle:
//
//	bamboo-support-<hostname>-<timestamp>.zip
//	├── manifest.json          # collection metadata
//	├── wg-state.txt           # local WireGuard interface dump
//	├── controller-healthz.txt # GET /healthz response
//	├── peer-list.json         # GET /api/v1/peers (when reachable)
//	├── env.txt                # BAMBOO_* env vars, secrets redacted
//	└── system.txt             # uname-equivalent + iface list
//
// Collection is best-effort: a missing piece is captured as a
// .error sibling file rather than aborting the bundle. The
// support engineer can then see "wg-state couldn't be read"
// instead of getting no bundle at all because one collector
// failed.

package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.zx2c4.com/wireguard/wgctrl"
)

var flagSupportBundleOutput string

var supportBundleCmd = &cobra.Command{
	Use:   "support-bundle",
	Short: "Collect diagnostic state into a ZIP for support tickets",
	Long: `support-bundle gathers local WireGuard interface state, the
controller's health + peer list, the client's environment (with
secrets redacted), and a small system snapshot into a single ZIP
file. Operators attach the bundle to a support ticket so the
bamboo team can reconstruct the client's state without a series
of back-and-forth requests for log files.

Secrets the bundle DOES NOT carry:
  - --auth-key value (pre-auth key secret)
  - WireGuard private key
  - BAMBOO_* env vars whose names contain SECRET, KEY, TOKEN, or
    PASSWORD (value redacted; name preserved)
  - The bamboo_session cookie value

The default output filename includes the hostname + an RFC3339-
style timestamp so multiple bundles from one host don't clobber
each other. --output overrides.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		path := flagSupportBundleOutput
		if path == "" {
			path = defaultBundlePath()
		}
		f, err := os.Create(path) //nolint:gosec // operator-supplied output path is the whole point of --output
		if err != nil {
			return fmt.Errorf("create bundle: %w", err)
		}
		defer func() { _ = f.Close() }()

		opts := bundleOptions{
			ControllerAddr: flagAddr,
			TenantSlug:     flagTenant,
			Interface:      flagIface,
			Env:            os.Environ(),
			Hostname:       defaultHostname(),
			HTTPClient:     &http.Client{Timeout: 3 * time.Second},
			WGDump:         readWGDump,
			NetIfaces:      net.Interfaces,
		}
		written, err := writeBundle(ctx, f, opts)
		if err != nil {
			return fmt.Errorf("write bundle: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d files, %d bytes)\n", path, written, fileSize(path))
		return nil
	},
}

func init() {
	supportBundleCmd.Flags().StringVarP(&flagSupportBundleOutput, "output", "o", "",
		"output ZIP path (default: bamboo-support-<hostname>-<timestamp>.zip in the current directory)")
	rootCmd.AddCommand(supportBundleCmd)
}

// defaultBundlePath returns a hostname-+-timestamp-bearing filename
// in the current directory. Multiple bundles from the same host
// don't clobber each other; sortable timestamp keeps the support
// engineer's `ls` output meaningful.
func defaultBundlePath() string {
	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	host := defaultHostname()
	return filepath.Join(".", fmt.Sprintf("bamboo-support-%s-%s.zip", host, ts))
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// readWGDump captures the wgctrl device state as plain text. Same
// fields the `bamboo status` command prints, formatted for human
// reading in the support bundle. Kept as a package-level var so
// tests can substitute a stub that doesn't need a real interface.
var readWGDump = func(iface string) (string, error) {
	c, err := wgctrl.New()
	if err != nil {
		return "", fmt.Errorf("wgctrl: %w", err)
	}
	defer func() { _ = c.Close() }()
	dev, err := c.Device(iface)
	if err != nil {
		return "", fmt.Errorf("device %s: %w", iface, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "interface: %s\n", dev.Name)
	fmt.Fprintf(&b, "  public key: %s\n", dev.PublicKey.String())
	fmt.Fprintf(&b, "  listen port: %d\n", dev.ListenPort)
	fmt.Fprintf(&b, "  peers: %d\n\n", len(dev.Peers))
	now := time.Now()
	for _, p := range dev.Peers {
		fmt.Fprintf(&b, "peer: %s\n", p.PublicKey.String())
		if p.Endpoint != nil {
			fmt.Fprintf(&b, "  endpoint: %s\n", p.Endpoint.String())
		}
		fmt.Fprintf(&b, "  allowed ips: %v\n", p.AllowedIPs)
		if !p.LastHandshakeTime.IsZero() {
			fmt.Fprintf(&b, "  last handshake: %s ago\n", now.Sub(p.LastHandshakeTime).Truncate(time.Second))
		} else {
			fmt.Fprintf(&b, "  last handshake: never\n")
		}
		fmt.Fprintf(&b, "  transfer: %d B received, %d B sent\n\n", p.ReceiveBytes, p.TransmitBytes)
	}
	return b.String(), nil
}

// bundleOptions is the DI struct for writeBundle. Kept verbose so
// tests can swap individual collectors (HTTPClient, WGDump,
// NetIfaces) without instantiating the cobra harness.
type bundleOptions struct {
	ControllerAddr string
	TenantSlug     string
	Interface      string
	Env            []string
	Hostname       string
	HTTPClient     *http.Client
	WGDump         func(iface string) (string, error)
	NetIfaces      func() ([]net.Interface, error)
}

// writeBundle is the pure-Go entry point that does all the work
// minus argument parsing. Returns the file count and an error if
// the ZIP writer itself failed; per-collector errors land as
// `<file>.error` siblings inside the bundle (best-effort
// collection over hard failure).
func writeBundle(ctx context.Context, w io.Writer, opts bundleOptions) (int, error) {
	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	manifest := map[string]any{
		"collectedAt":    time.Now().UTC().Format(time.RFC3339),
		"hostname":       opts.Hostname,
		"bambooVersion":  Version,
		"bambooCommit":   Commit,
		"bambooBuildAt":  BuildDate,
		"goOS":           runtime.GOOS,
		"goArch":         runtime.GOARCH,
		"controllerAddr": opts.ControllerAddr,
		"tenantSlug":     opts.TenantSlug,
		"interface":      opts.Interface,
	}
	if err := addJSON(zw, "manifest.json", manifest); err != nil {
		return 0, fmt.Errorf("manifest: %w", err)
	}

	collectors := []struct {
		name string
		run  func() (string, error)
	}{
		{"wg-state.txt", func() (string, error) {
			if opts.WGDump == nil {
				return "", fmt.Errorf("wg collector not configured")
			}
			return opts.WGDump(opts.Interface)
		}},
		{"controller-healthz.txt", func() (string, error) {
			return fetchControllerHealthz(ctx, opts.HTTPClient, opts.ControllerAddr)
		}},
		{"peer-list.json", func() (string, error) {
			return fetchPeerList(ctx, opts.HTTPClient, opts.ControllerAddr, opts.TenantSlug)
		}},
		{"env.txt", func() (string, error) {
			return RedactEnv(opts.Env), nil
		}},
		{"system.txt", func() (string, error) {
			return collectSystem(opts.NetIfaces), nil
		}},
	}

	written := 1 // manifest
	for _, c := range collectors {
		body, err := c.run()
		if err != nil {
			if werr := addString(zw, c.name+".error", err.Error()); werr != nil {
				return written, fmt.Errorf("write %s.error: %w", c.name, werr)
			}
			written++
			continue
		}
		if werr := addString(zw, c.name, body); werr != nil {
			return written, fmt.Errorf("write %s: %w", c.name, werr)
		}
		written++
	}
	if err := zw.Close(); err != nil {
		return written, fmt.Errorf("close zip: %w", err)
	}
	return written, nil
}

func addJSON(zw *zip.Writer, name string, v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return addString(zw, name, string(body)+"\n")
}

func addString(zw *zip.Writer, name, body string) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, body)
	return err
}

// fetchControllerHealthz hits /healthz and returns "<status> <body>".
// 3s timeout via the HTTP client; an unreachable controller surfaces
// as the collector's error path (".error" sibling in the bundle)
// rather than failing the bundle.
func fetchControllerHealthz(ctx context.Context, client *http.Client, addr string) (string, error) {
	url := "http://" + addr + "/healthz"
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		url = strings.TrimRight(addr, "/") + "/healthz"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return fmt.Sprintf("status: %d\n\n%s\n", resp.StatusCode, string(body)), nil
}

// fetchPeerList GETs /api/v1/peers using the supplied tenant slug.
// Sends X-Tenant-Slug because the support-bundle path is intended
// for unauthenticated dev / self-hosted scenarios; the operator
// can edit the bundle to add credentials before sharing if their
// controller runs in prod-auth mode (the bundle would otherwise
// carry a captured 401 body, which is also a useful signal).
func fetchPeerList(ctx context.Context, client *http.Client, addr, tenant string) (string, error) {
	url := "http://" + addr + "/api/v1/peers"
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		url = strings.TrimRight(addr, "/") + "/api/v1/peers"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-Slug", tenant)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("status: %d\n\n%s\n", resp.StatusCode, string(body)), nil
	}
	return string(body), nil
}

// collectSystem records basic host info — Go runtime version,
// OS / arch, and a list of network interface names + addrs. This
// is the "is the machine on the network at all" sanity check
// that often shortcuts a back-and-forth in a support ticket.
func collectSystem(ifacesFn func() ([]net.Interface, error)) string {
	var b strings.Builder
	fmt.Fprintf(&b, "go version: %s\n", runtime.Version())
	fmt.Fprintf(&b, "os/arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "collected:  %s\n\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "network interfaces:\n")
	if ifacesFn == nil {
		fmt.Fprintln(&b, "  <unavailable>")
		return b.String()
	}
	ifaces, err := ifacesFn()
	if err != nil {
		fmt.Fprintf(&b, "  error: %v\n", err)
		return b.String()
	}
	for _, ifc := range ifaces {
		fmt.Fprintf(&b, "  %s (flags=%s, mtu=%d)\n", ifc.Name, ifc.Flags, ifc.MTU)
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			fmt.Fprintf(&b, "    %s\n", a.String())
		}
	}
	return b.String()
}
