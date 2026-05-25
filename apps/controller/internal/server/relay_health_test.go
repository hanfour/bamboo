// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProbeRelayHealth_HappyPath pins the contract a real relay
// will hit: 200 OK on /healthz ⇒ ("healthy", "") so the reaper
// records no error and ListEligible keeps the row.
func TestProbeRelayHealth_HappyPath(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %q, want /healthz", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := srv.Client() // trusts the test TLS cert; reaper uses its own client in prod
	status, errMsg := probeRelayHealth(context.Background(), client, host, port)
	if status != "healthy" {
		t.Errorf("status = %q, want healthy", status)
	}
	if errMsg != "" {
		t.Errorf("errMsg = %q, want empty on healthy", errMsg)
	}
}

// TestProbeRelayHealth_Non2xx pins the failure mode: a relay
// answering /healthz with 5xx (e.g. it's up but its backend is
// degraded) must be flagged unhealthy with a short reason.
func TestProbeRelayHealth_Non2xx(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	status, errMsg := probeRelayHealth(context.Background(), srv.Client(), host, port)
	if status != "unhealthy" {
		t.Errorf("status = %q, want unhealthy", status)
	}
	if errMsg != "status 503" {
		t.Errorf("errMsg = %q, want 'status 503'", errMsg)
	}
}

// TestProbeRelayHealth_Unreachable pins the network-fail case:
// dialing a port nothing listens on must surface as 'unhealthy'
// with a short cause string. Without this a misbehaving relay
// would silently stay in the eligible list because its row's
// status defaults to NULL.
func TestProbeRelayHealth_Unreachable(t *testing.T) {
	// Grab an ephemeral port then immediately close the listener
	// so nothing's bound. The probe's TCP SYN gets RST.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	client := newRelayHealthClient()
	status, errMsg := probeRelayHealth(context.Background(), client, "127.0.0.1", port)
	if status != "unhealthy" {
		t.Errorf("status = %q, want unhealthy", status)
	}
	if errMsg == "" {
		t.Errorf("errMsg empty, want a non-empty cause")
	}
}

// TestProbeRelayHealth_Timeout pins the slow-relay case: a relay
// that accepts the connection but never sends a response (the
// "half-up" failure mode our DisableKeepAlives notes call out) must
// time out at relayHealthProbeTimeout and report a short cause.
func TestProbeRelayHealth_Timeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hold until client times out
	}))
	defer srv.Close()

	// Shrink the timeout for the test so it stays fast.
	orig := relayHealthProbeTimeout
	relayHealthProbeTimeout = 150 * time.Millisecond
	defer func() { relayHealthProbeTimeout = orig }()

	host, port := splitHostPort(t, srv.URL)
	start := time.Now()
	status, errMsg := probeRelayHealth(context.Background(), srv.Client(), host, port)
	elapsed := time.Since(start)
	if status != "unhealthy" {
		t.Errorf("status = %q, want unhealthy", status)
	}
	if errMsg == "" {
		t.Errorf("errMsg empty, want a timeout-ish cause")
	}
	// 1s ceiling — anything longer means the timeout didn't trip.
	if elapsed > time.Second {
		t.Errorf("probe took %v, want under 1s with 150ms timeout", elapsed)
	}
}

// TestTrimErrForUI ensures the URL prefix in net/http error
// strings gets stripped so the LastHealthError column stays
// compact. Otherwise the admin UI table cell renders something
// like 'Get "https://relay-east.example.com:8443/healthz":
// context deadline exceeded' — useful but redundant.
// TestRelayEligibilityChanged is the truth table for the §4 P2
// stage 4a eligibility-flip detection. Every cell of the 3x3 matrix
// gets a row because a subtle bug here would either silently
// spam every WatchPeers subscriber every 30s (false positives) or
// silently fail to notify them when a relay flipped (false
// negatives). The matrix is small enough to enumerate fully.
func TestRelayEligibilityChanged(t *testing.T) {
	cases := []struct {
		prev, next string
		want       bool
		why        string
	}{
		// Both eligible → no change.
		{"unknown", "unknown", false, "no-op"},
		{"unknown", "healthy", false, "unknown→healthy: both eligible, set unchanged"},
		{"healthy", "unknown", false, "healthy→unknown: both eligible"},
		{"healthy", "healthy", false, "no-op"},
		{"", "healthy", false, "empty→healthy: empty normalises to unknown, both eligible"},
		// Both ineligible → no change (degenerate but the function should still say false).
		{"unhealthy", "unhealthy", false, "no-op"},
		// Flip into ineligibility.
		{"unknown", "unhealthy", true, "first probe failed: relay drops out of eligible list"},
		{"healthy", "unhealthy", true, "regression: relay drops out of eligible list"},
		{"", "unhealthy", true, "empty→unhealthy: same as unknown→unhealthy"},
		// Flip back to eligibility.
		{"unhealthy", "healthy", true, "recovered: relay re-enters eligible list"},
		{"unhealthy", "unknown", true, "shouldn't normally happen but covers the row"},
	}
	for _, tc := range cases {
		t.Run(tc.prev+"→"+tc.next, func(t *testing.T) {
			got := relayEligibilityChanged(tc.prev, tc.next)
			if got != tc.want {
				t.Errorf("relayEligibilityChanged(%q, %q) = %v, want %v — %s",
					tc.prev, tc.next, got, tc.want, tc.why)
			}
		})
	}
}

func TestTrimErrForUI(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`Get "https://relay-east.example.com:8443/healthz": context deadline exceeded`,
			"context deadline exceeded"},
		{`Get "https://x:1/healthz": dial tcp 127.0.0.1:1: connect: connection refused`,
			"connection refused"},
		{"no-colon-no-trim", "no-colon-no-trim"},
	}
	for _, c := range cases {
		got := trimErrForUI(&simpleErr{msg: c.in})
		if got != c.want {
			t.Errorf("trimErrForUI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func splitHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(u.Host, ":") {
		_, p, err := net.SplitHostPort(u.Host)
		if err == nil {
			n, _ := strconv.Atoi(p)
			return host, n
		}
	}
	return host, 443
}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
