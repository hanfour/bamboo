// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"
	"testing"
)

func TestProxy_UntrustedPeerIgnoresForgedXFF(t *testing.T) {
	up := &captureUpstream{}
	lim := DefaultLimits()
	lim.PerClientPerMinute = 1
	front, _, rec := setupTrusting(t, up, lim, nil)

	if r := get(t, front, testHost, map[string]string{"X-Forwarded-For": "203.0.113.5"}); r.StatusCode != 200 {
		t.Fatalf("first: %d", r.StatusCode)
	}
	if v := up.last().Header.Values("X-Forwarded-For"); len(v) != 1 || v[0] != "127.0.0.1" {
		t.Errorf("upstream X-Forwarded-For = %q, want only the socket ip", v)
	}
	if recs := rec.all(); len(recs) != 1 || recs[0].ClientIP != "127.0.0.1" {
		t.Errorf("telemetry client ip = %+v, want 127.0.0.1", recs)
	}
	// A fresh forged address does not buy a fresh bucket: the limiter is
	// keyed on the socket ip.
	if r := get(t, front, testHost, map[string]string{"X-Forwarded-For": "203.0.113.6"}); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second with new forged XFF: %d, want 429", r.StatusCode)
	}
}

func TestProxy_TrustedPeerKeepsChain(t *testing.T) {
	up := &captureUpstream{}
	front, _, rec := setupTrusting(t, up, DefaultLimits(), loopback)
	get(t, front, testHost, map[string]string{"X-Forwarded-For": "198.51.100.1, 203.0.113.5"})
	if v := up.last().Header.Get("X-Forwarded-For"); v != "198.51.100.1, 203.0.113.5" {
		t.Errorf("upstream X-Forwarded-For = %q", v)
	}
	if recs := rec.all(); len(recs) != 1 || recs[0].ClientIP != "203.0.113.5" {
		t.Errorf("telemetry client ip = %+v", recs)
	}
}

func TestProxy_RejectsUpgrade(t *testing.T) {
	for name, hdr := range map[string]map[string]string{
		"websocket":        {"Connection": "Upgrade", "Upgrade": "websocket"},
		"upgrade only":     {"Upgrade": "h2c"},
		"connection token": {"Connection": "keep-alive, upgrade"},
	} {
		t.Run(name, func(t *testing.T) {
			up := &captureUpstream{}
			front, _, _ := setup(t, up, DefaultLimits())
			r := get(t, front, testHost, hdr)
			if r.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", r.StatusCode)
			}
			if up.last() != nil {
				t.Error("upgrade request reached the upstream")
			}
		})
	}
}

func TestProxy_IPv6ClientsShareTheirSlash64(t *testing.T) {
	lim := DefaultLimits()
	lim.PerClientPerMinute = 1
	front, _, _ := setup(t, &captureUpstream{}, lim)
	xff := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }

	if r := get(t, front, testHost, xff("2001:db8:1:2::1")); r.StatusCode != 200 {
		t.Fatalf("first: %d", r.StatusCode)
	}
	if r := get(t, front, testHost, xff("2001:db8:1:2:ffff::9")); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("same /64: %d, want 429", r.StatusCode)
	}
	if r := get(t, front, testHost, xff("2001:db8:1:3::1")); r.StatusCode != 200 {
		t.Errorf("other /64: %d, want 200", r.StatusCode)
	}
	if r := get(t, front, testHost, xff("192.0.2.1")); r.StatusCode != 200 {
		t.Fatalf("ipv4 a: %d", r.StatusCode)
	}
	if r := get(t, front, testHost, xff("192.0.2.2")); r.StatusCode != 200 {
		t.Errorf("ipv4 neighbour: %d, want 200 (ipv4 keyed by full address)", r.StatusCode)
	}
}

func TestLimiterKey(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":            "ip:192.0.2.7",
		"2001:db8:1:2:3:4:5:6": "ip:2001:db8:1:2::/64",
		"::ffff:192.0.2.7":     "ip:192.0.2.7",
		"not-an-ip":            "ip:not-an-ip",
		"2001:db8:1:2::1%eth0": "ip:2001:db8:1:2::/64",
	} {
		if got := limiterKey(in); got != want {
			t.Errorf("limiterKey(%q) = %q, want %q", in, got, want)
		}
	}
}
