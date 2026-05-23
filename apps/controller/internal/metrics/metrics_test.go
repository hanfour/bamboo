// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/metrics"
)

// TestRegistry_BuildInfoLabels pins the label semantics: a freshly-
// constructed registry exposes bamboo_build_info=1 with the
// (version, commit) labels supplied to NewRegistry. SetBuildInfo
// resets the series so a deploy that changes the version string
// doesn't leave a stale "always 1" series of the previous version.
func TestRegistry_BuildInfoLabels(t *testing.T) {
	r := metrics.NewRegistry("v1.0.0", "abc123")
	body := scrape(t, r)
	if !strings.Contains(body, `bamboo_build_info{commit="abc123",version="v1.0.0"} 1`) {
		t.Errorf("scrape missing build_info series; body=\n%s", body)
	}

	r.SetBuildInfo("v1.1.0", "def456")
	body = scrape(t, r)
	if !strings.Contains(body, `bamboo_build_info{commit="def456",version="v1.1.0"} 1`) {
		t.Errorf("scrape missing updated build_info; body=\n%s", body)
	}
	// The previous version's series must be GONE — dashboards keyed
	// on "count by version" rely on Reset() wiping the old labels.
	if strings.Contains(body, `bamboo_build_info{commit="abc123",version="v1.0.0"}`) {
		t.Errorf("scrape still carries previous version; body=\n%s", body)
	}
}

// TestRegistry_EmptyBuildInfoFallsBackToUnknown verifies the empty-
// string fallback. The production wiring may call NewRegistry before
// the cmd entrypoint has plumbed in the ldflags-injected values; in
// that window the scrape shouldn't expose an empty label.
func TestRegistry_EmptyBuildInfoFallsBackToUnknown(t *testing.T) {
	r := metrics.NewRegistry("", "")
	body := scrape(t, r)
	if !strings.Contains(body, `bamboo_build_info{commit="unknown",version="unknown"} 1`) {
		t.Errorf("scrape missing fallback build_info; body=\n%s", body)
	}
}

// TestRegistry_CounterIncrementsAreVisibleOnScrape covers the
// canonical write-then-scrape round trip on a representative counter
// — without this, a typo in the metric name or the Vec wiring would
// only surface at production-scrape time.
func TestRegistry_CounterIncrementsAreVisibleOnScrape(t *testing.T) {
	r := metrics.NewRegistry("v1.0.0", "abc123")
	r.RegisterTotal.WithLabelValues("success").Inc()
	r.RegisterTotal.WithLabelValues("success").Inc()
	r.RegisterTotal.WithLabelValues("denied").Inc()
	body := scrape(t, r)
	if !strings.Contains(body, `bamboo_register_total{outcome="success"} 2`) {
		t.Errorf("missing success counter; body=\n%s", body)
	}
	if !strings.Contains(body, `bamboo_register_total{outcome="denied"} 1`) {
		t.Errorf("missing denied counter; body=\n%s", body)
	}
}

// TestRegistry_WatchStreamsActiveGauge pins the Inc/Dec balance —
// a leak here is the kind of bug that goes unnoticed for weeks and
// then shows up as "we have 4 million watch streams" on a dashboard.
func TestRegistry_WatchStreamsActiveGauge(t *testing.T) {
	r := metrics.NewRegistry("v1.0.0", "abc123")
	r.WatchStreamsActive.Inc()
	r.WatchStreamsActive.Inc()
	body := scrape(t, r)
	if !strings.Contains(body, `bamboo_watch_streams_active 2`) {
		t.Errorf("expected gauge = 2; body=\n%s", body)
	}
	r.WatchStreamsActive.Dec()
	r.WatchStreamsActive.Dec()
	body = scrape(t, r)
	if !strings.Contains(body, `bamboo_watch_streams_active 0`) {
		t.Errorf("expected gauge = 0 after two Decs; body=\n%s", body)
	}
}

// TestRegistry_NilHandlerDegradesTo404 verifies the optional-
// registry contract. Server-package callers may mount the handler
// before deciding whether to enable metrics; a nil receiver must
// not panic.
func TestRegistry_NilHandlerDegradesTo404(t *testing.T) {
	var r *metrics.Registry
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 404 {
		t.Errorf("nil registry: status = %d, want 404", rec.Code)
	}
}

// TestStatusFamily covers the cardinality-saver helper: every
// status code outside the standard 1xx-5xx range should fall back
// to "other" so a malformed handler can't pollute the time-series
// space with a literal int label.
func TestStatusFamily(t *testing.T) {
	cases := map[int]string{
		200: "2xx", 204: "2xx", 299: "2xx",
		301: "3xx", 304: "3xx",
		400: "4xx", 401: "4xx", 404: "4xx", 499: "4xx",
		500: "5xx", 503: "5xx", 599: "5xx",
		0: "other", 100: "other", 600: "other", 999: "other",
	}
	for code, want := range cases {
		if got := metrics.StatusFamily(code); got != want {
			t.Errorf("StatusFamily(%d) = %q, want %q", code, got, want)
		}
	}
}

// scrape exercises the Handler the same way Prometheus would and
// returns the response body as a string so test assertions can
// substring-match the text exposition format.
func scrape(t *testing.T, r *metrics.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("scrape status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
