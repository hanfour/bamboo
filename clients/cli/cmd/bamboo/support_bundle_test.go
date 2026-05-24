// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRedactEnv_KeepsBambooAndRedactsSecrets pins the grammar
// documented at RedactEnv: only BAMBOO_* vars survive; names
// matching the secret-like substring set get their values
// replaced with "<redacted, N chars>". The N preserves shape
// info (empty vs typo'd vs full token) without leaking the
// secret itself.
func TestRedactEnv_KeepsBambooAndRedactsSecrets(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",               // dropped: not BAMBOO_*
		"HOME=/home/han",              // dropped: not BAMBOO_*
		"SSH_AUTH_SOCK=/tmp/whatever", // dropped: not BAMBOO_*
		"BAMBOO_TENANT=default",       // kept verbatim
		"BAMBOO_ADDR=127.0.0.1:8080",  // kept verbatim
		"BAMBOO_SESSION_SECRET=this-is-32-bytes-of-secret-yes", // redacted (SECRET in name)
		"BAMBOO_AUTH_KEY=bka_abc_def",                          // redacted (KEY in name)
		"BAMBOO_SESSION_TOKEN=ey...",                           // redacted (TOKEN in name)
		"BAMBOO_DB_PASSWORD=hunter2",                           // redacted (PASSWORD in name)
		"BAMBOO_BAMBOO_COOKIE=anything",                        // redacted (COOKIE in name)
		"BAMBOO_EMPTY_TOKEN=",                                  // redacted (TOKEN in name), empty
	}
	out := RedactEnv(env)
	mustContain := []string{
		"BAMBOO_TENANT=default",
		"BAMBOO_ADDR=127.0.0.1:8080",
		"BAMBOO_SESSION_SECRET=<redacted, 30 chars>",
		"BAMBOO_AUTH_KEY=<redacted, 11 chars>",
		"BAMBOO_SESSION_TOKEN=<redacted, 5 chars>",
		"BAMBOO_DB_PASSWORD=<redacted, 7 chars>",
		"BAMBOO_BAMBOO_COOKIE=<redacted, 8 chars>",
		"BAMBOO_EMPTY_TOKEN=<redacted, empty>",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, out)
		}
	}
	mustNotContain := []string{
		"PATH=", "HOME=", "SSH_AUTH_SOCK=",
		"this-is-32-bytes-of-secret-yes", "bka_abc_def",
		"ey...", "hunter2", "anything",
	}
	for _, denied := range mustNotContain {
		if strings.Contains(out, denied) {
			t.Errorf("output leaked %q\noutput:\n%s", denied, out)
		}
	}
}

// TestRedactEnv_OrderingIsDeterministic pins the sort key — two
// runs over the same env produce identical output so support
// engineers can diff bundles cleanly without sort-order noise.
func TestRedactEnv_OrderingIsDeterministic(t *testing.T) {
	env := []string{
		"BAMBOO_Z=last",
		"BAMBOO_A=first",
		"BAMBOO_M=middle",
	}
	first := RedactEnv(env)
	// Run again with input shuffled — output must still match.
	second := RedactEnv([]string{"BAMBOO_M=middle", "BAMBOO_Z=last", "BAMBOO_A=first"})
	if first != second {
		t.Errorf("non-deterministic output:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.HasPrefix(first, "BAMBOO_A=") {
		t.Errorf("first row should be BAMBOO_A= (lexicographic); got:\n%s", first)
	}
}

// TestIsSecretLikeName covers the substring matcher independently
// of the env-loop wrapper. Documented marker set: SECRET / KEY /
// TOKEN / PASSWORD / COOKIE (case-insensitive substring match).
func TestIsSecretLikeName(t *testing.T) {
	cases := map[string]bool{
		"BAMBOO_SECRET":        true,
		"BAMBOO_session_token": true,
		"BAMBOO_PASSWORD":      true,
		"BAMBOO_API_KEY":       true,
		"BAMBOO_COOKIE_NAME":   true, // contains COOKIE
		"BAMBOO_TENANT":        false,
		"BAMBOO_ADDR":          false,
		"BAMBOO_HOSTNAME":      false,
		"BAMBOO_LOG_JSON":      false,
		"BAMBOO_KEYSPACE":      true, // KEY substring — false positive accepted in the grammar
	}
	for name, want := range cases {
		if got := isSecretLikeName(name); got != want {
			t.Errorf("isSecretLikeName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestWriteBundle_HappyPath drives the end-to-end ZIP path with
// stubbed collectors so the test stays hermetic (no DB, no
// network, no wgctrl). Asserts:
//
//   - manifest.json exists and carries the hostname + version
//   - every named collector contributed either its file or its
//     .error sibling
//   - the controller-* files reflect what the stub HTTP server
//     served
//   - env.txt is redacted per the documented grammar
func TestWriteBundle_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, "ok")
		case "/api/v1/peers":
			_, _ = io.WriteString(w, `{"peers":[{"hostname":"alice"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	var buf bytes.Buffer
	written, err := writeBundle(context.Background(), &buf, bundleOptions{
		ControllerAddr: addr,
		TenantSlug:     "default",
		Interface:      "bamboo0",
		Env: []string{
			"BAMBOO_TENANT=default",
			"BAMBOO_SESSION_SECRET=keep-out-of-bundle",
			"HOME=/notincluded",
		},
		Hostname:   "test-host",
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		WGDump: func(iface string) (string, error) {
			return fmt.Sprintf("interface: %s\n  peers: 0\n", iface), nil
		},
		NetIfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "lo0", MTU: 16384}}, nil
		},
	})
	if err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
	if written < 5 {
		t.Errorf("expected at least 5 bundle entries, got %d", written)
	}

	entries := readBundle(t, buf.Bytes())
	for _, name := range []string{
		"manifest.json", "wg-state.txt",
		"controller-healthz.txt", "peer-list.json",
		"env.txt", "system.txt",
	} {
		if _, ok := entries[name]; !ok {
			t.Errorf("bundle missing %q (entries: %v)", name, keys(entries))
		}
	}

	if !strings.Contains(entries["manifest.json"], `"hostname": "test-host"`) {
		t.Errorf("manifest didn't carry hostname:\n%s", entries["manifest.json"])
	}
	if !strings.Contains(entries["wg-state.txt"], "interface: bamboo0") {
		t.Errorf("wg-state.txt didn't carry interface name:\n%s", entries["wg-state.txt"])
	}
	if !strings.Contains(entries["controller-healthz.txt"], "status: 200") ||
		!strings.Contains(entries["controller-healthz.txt"], "ok") {
		t.Errorf("controller-healthz.txt unexpected:\n%s", entries["controller-healthz.txt"])
	}
	if !strings.Contains(entries["peer-list.json"], "alice") {
		t.Errorf("peer-list.json missing peer data:\n%s", entries["peer-list.json"])
	}
	// env.txt: kept BAMBOO_TENANT verbatim, redacted SECRET, dropped HOME.
	if !strings.Contains(entries["env.txt"], "BAMBOO_TENANT=default") {
		t.Errorf("env.txt missing tenant:\n%s", entries["env.txt"])
	}
	if strings.Contains(entries["env.txt"], "keep-out-of-bundle") {
		t.Errorf("env.txt LEAKED secret value:\n%s", entries["env.txt"])
	}
	if strings.Contains(entries["env.txt"], "HOME=") {
		t.Errorf("env.txt included non-BAMBOO_ var:\n%s", entries["env.txt"])
	}
	if !strings.Contains(entries["system.txt"], "lo0") {
		t.Errorf("system.txt missing iface:\n%s", entries["system.txt"])
	}
}

// TestWriteBundle_CollectorFailureLandsAsErrorSibling proves the
// best-effort-collection contract: a collector that errors out
// doesn't kill the bundle. The bundle ships with a "<name>.error"
// sibling carrying the error message so the support engineer
// sees what couldn't be collected instead of getting no bundle.
func TestWriteBundle_CollectorFailureLandsAsErrorSibling(t *testing.T) {
	// HTTP server that's unreachable from the get-go.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	srv.Close() // close before the test runs — controller-* fetch fails.

	var buf bytes.Buffer
	_, err := writeBundle(context.Background(), &buf, bundleOptions{
		ControllerAddr: strings.TrimPrefix(srv.URL, "http://"),
		TenantSlug:     "default",
		Interface:      "bamboo0",
		Env:            []string{"BAMBOO_TENANT=default"},
		Hostname:       "broken-host",
		HTTPClient:     &http.Client{Timeout: 500 * time.Millisecond},
		WGDump: func(string) (string, error) {
			return "", fmt.Errorf("wgctrl: interface not found")
		},
		NetIfaces: func() ([]net.Interface, error) { return nil, fmt.Errorf("permission denied") },
	})
	if err != nil {
		t.Fatalf("writeBundle: %v (collector failures should NOT abort the bundle)", err)
	}
	entries := readBundle(t, buf.Bytes())
	// Manifest + env always succeed. The other collectors error.
	for _, name := range []string{
		"wg-state.txt.error",
		"controller-healthz.txt.error",
		"peer-list.json.error",
	} {
		if _, ok := entries[name]; !ok {
			t.Errorf("expected %q in bundle (collector failed); entries: %v", name, keys(entries))
		}
	}
	// And the successful collectors landed normally.
	if _, ok := entries["manifest.json"]; !ok {
		t.Errorf("manifest.json missing")
	}
	if _, ok := entries["env.txt"]; !ok {
		t.Errorf("env.txt missing")
	}
	// system.txt's iface-listing helper handles the error itself
	// (it captures the error inline) so we still get a system.txt,
	// not a system.txt.error.
	if _, ok := entries["system.txt"]; !ok {
		t.Errorf("system.txt missing")
	}
	if !strings.Contains(entries["system.txt"], "permission denied") {
		t.Errorf("system.txt didn't capture iface error:\n%s", entries["system.txt"])
	}
}

func readBundle(t *testing.T, body []byte) map[string]string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := make(map[string]string, len(r.File))
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		buf, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		out[f.Name] = string(buf)
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
