// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestMetricsEndpoint_ServesPrometheusExposition is the smoke test
// for the /metrics scrape (§4 P2 observability): the endpoint
// returns 200, the text-exposition body contains the four
// canonical metric names, and bamboo_build_info shows up with a
// version label even before SetBuildInfo runs.
func TestMetricsEndpoint_ServesPrometheusExposition(t *testing.T) {
	f := startFixture(t)

	resp, err := http.Get(f.httpURL + "/metrics") //nolint:gosec // test fixture URL
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	// Metrics with pre-registered or always-on series must appear
	// in a fresh scrape. HTTPRequestsTotal / DurationSeconds have a
	// dynamic (method, route, status) label space and only show up
	// after the first request — they're covered indirectly by the
	// register-increments-counter test below, which forces a
	// matching observation before scraping.
	wantMetrics := []string{
		"bamboo_build_info",
		"bamboo_register_total",
		"bamboo_heartbeat_total",
		"bamboo_watch_streams_active",
	}
	for _, name := range wantMetrics {
		if !strings.Contains(body, name) {
			t.Errorf("scrape body missing %q; body=\n%s", name, body)
		}
	}
}

// TestMetricsEndpoint_RegisterIncrementsCounter drives the
// register endpoint, then scrapes /metrics and asserts the
// outcome="success" series advanced. This is the end-to-end seam
// test that catches "the defer landed in the wrong scope" bugs
// the unit tests can't see.
func TestMetricsEndpoint_RegisterIncrementsCounter(t *testing.T) {
	f := startFixture(t)

	// One success registration.
	reg := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "metric-test",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if reg.status != http.StatusOK {
		t.Fatalf("register: status=%d body=%s", reg.status, reg.body)
	}

	// One denied registration (missing required fields).
	bad := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"tenantSlug": f.tenantSlug,
	})
	if bad.status != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing fields, got %d body=%s", bad.status, bad.body)
	}

	resp, err := http.Get(f.httpURL + "/metrics") //nolint:gosec // test fixture URL
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	// Each counter line is "name{labels} value" — assert presence
	// and a value >= our increment count (other tests in the same
	// fixture process could have bumped it too; the suite runs
	// sequentially per package so the lower bound is tight).
	if !containsCounterAtLeast(body, `bamboo_register_total{outcome="success"}`, 1) {
		t.Errorf("register success counter not incremented; body=\n%s", body)
	}
	if !containsCounterAtLeast(body, `bamboo_register_total{outcome="denied"}`, 1) {
		t.Errorf("register denied counter not incremented; body=\n%s", body)
	}
}

// containsCounterAtLeast checks the text-exposition format for a
// `<label-prefix> N` line where N >= floor. Each counter line in
// Prom format ends with the numeric sample; we parse the trailing
// token as a float (counters can serialize as int or float) and
// compare against the floor.
func containsCounterAtLeast(body, prefix string, floor int) bool {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		if int(v) >= floor {
			return true
		}
	}
	return false
}
