// SPDX-License-Identifier: AGPL-3.0-or-later

// Package metrics holds the Prometheus instrumentation for the
// controller (§4 P2 observability). Initial set is intentionally
// narrow — the operator pains today are "is the controller up" and
// "is the register / heartbeat hot path healthy", both answerable
// without high-cardinality DB-backed gauges. Per-tenant gauges
// (peer counts, policy revisions) are a follow-up so we don't pay
// scrape-time DB cost before we know which series matter.
//
// Naming follows the Prom convention:
//   - All metrics share the bamboo_ prefix.
//   - Histograms expose _seconds (not _ms).
//   - Counters end in _total per Prometheus best-practice.
//   - bamboo_build_info is the canonical "is this process up + which
//     version" gauge (always 1; labels carry version + commit).
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry bundles the controller's metric set with a private
// prometheus.Registerer. Using a fresh Registerer (not the global
// prometheus.DefaultRegisterer) keeps tests isolated: each test
// constructs its own Registry, asserts against it, and tears it
// down without leaking metric definitions between tests.
type Registry struct {
	reg *prometheus.Registry

	// BuildInfo is the always-1 gauge that lets dashboards count
	// running instances and group them by version / commit. Operators
	// often pair this with a rate(bamboo_http_requests_total) panel
	// keyed on version to spot bad deploys.
	BuildInfo *prometheus.GaugeVec

	// HTTPRequestsTotal counts every JSON REST + auth-redirect request
	// that hits the HTTP server. Labels: method, route (pattern, not
	// exact path — paths carry UUIDs that would explode cardinality),
	// status (HTTP status family: "2xx" / "3xx" / "4xx" / "5xx").
	HTTPRequestsTotal *prometheus.CounterVec

	// HTTPRequestDurationSeconds is the latency histogram. Same
	// (method, route) label pair as HTTPRequestsTotal. Status is
	// dropped — latency rarely differs by status family enough to
	// justify doubling the series count.
	HTTPRequestDurationSeconds *prometheus.HistogramVec

	// RegisterTotal counts /api/v1/peers/register attempts by outcome.
	// Distinct from HTTPRequestsTotal because the outcome we care
	// about is application-level: did this register succeed, get
	// rejected by the auth gate, or fail due to a server-side error.
	RegisterTotal *prometheus.CounterVec

	// HeartbeatTotal mirrors RegisterTotal for the heartbeat path,
	// which is the highest-rate endpoint the controller serves.
	HeartbeatTotal *prometheus.CounterVec

	// WatchStreamsActive is the gauge of currently-open WatchPeers
	// SSE streams across all tenants. Incremented when a stream
	// opens, decremented when it closes (regardless of reason —
	// client disconnect, server shutdown, idle timeout).
	WatchStreamsActive prometheus.Gauge
}

// NewRegistry constructs the Registry with all metrics registered.
// version + commit populate the build_info gauge; both fall back to
// "unknown" when the build pipeline doesn't inject them.
func NewRegistry(version, commit string) *Registry {
	r := &Registry{
		reg: prometheus.NewRegistry(),
		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "bamboo_build_info",
			Help: "Always 1; labels carry the build's version + commit. Useful for grouping panels by deploy.",
		}, []string{"version", "commit"}),
		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bamboo_http_requests_total",
			Help: "REST + auth-redirect requests served, labelled by method, route pattern, and HTTP status family.",
		}, []string{"method", "route", "status"}),
		HTTPRequestDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "bamboo_http_request_duration_seconds",
			Help:    "Latency of REST + auth-redirect requests.",
			Buckets: prometheus.ExponentialBucketsRange(0.001, 5, 14),
		}, []string{"method", "route"}),
		RegisterTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bamboo_register_total",
			Help: "POST /api/v1/peers/register attempts, labelled by outcome (success|denied|error).",
		}, []string{"outcome"}),
		HeartbeatTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bamboo_heartbeat_total",
			Help: "POST /api/v1/peers/heartbeat attempts, labelled by outcome (success|denied|error).",
		}, []string{"outcome"}),
		WatchStreamsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bamboo_watch_streams_active",
			Help: "Currently-open Server-Sent Events WatchPeers streams across all tenants.",
		}),
	}
	r.reg.MustRegister(
		r.BuildInfo,
		r.HTTPRequestsTotal,
		r.HTTPRequestDurationSeconds,
		r.RegisterTotal,
		r.HeartbeatTotal,
		r.WatchStreamsActive,
	)
	if version == "" {
		version = "unknown"
	}
	if commit == "" {
		commit = "unknown"
	}
	r.BuildInfo.WithLabelValues(version, commit).Set(1)
	// Pre-register the bounded-cardinality counter labels at zero so
	// the scrape exposes the series before the first event lands.
	// Without this, Prometheus alerts of the form rate(...) ==
	// bool(0) misbehave during a quiet window — the series simply
	// doesn't exist yet, so rate() returns NaN rather than 0, and
	// alert rules that check "is this counter alive at all"
	// silently fail. The (method, route, status) label space on
	// HTTPRequestsTotal is too wide to enumerate, so it stays lazy.
	for _, outcome := range []string{"success", "denied", "error"} {
		r.RegisterTotal.WithLabelValues(outcome)
		r.HeartbeatTotal.WithLabelValues(outcome)
	}
	return r
}

// SetBuildInfo overwrites the bamboo_build_info gauge with fresh
// (version, commit) labels and clears any previous label set so
// dashboards don't see two "always 1" series after a deploy that
// changes the version string. Safe to call multiple times; safe on
// a nil Registry (no-op).
func (r *Registry) SetBuildInfo(version, commit string) {
	if r == nil || r.BuildInfo == nil {
		return
	}
	// Reset wipes ALL label combinations registered under this Vec;
	// the next WithLabelValues call adds the fresh series.
	r.BuildInfo.Reset()
	if version == "" {
		version = "unknown"
	}
	if commit == "" {
		commit = "unknown"
	}
	r.BuildInfo.WithLabelValues(version, commit).Set(1)
}

// Handler returns the /metrics HTTP handler that scrapes use.
// Returns a 404-style empty body when Registry is nil so callers
// holding an optional reference can mount it unconditionally.
func (r *Registry) Handler() http.Handler {
	if r == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics disabled", http.StatusNotFound)
		})
	}
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		// EnableOpenMetrics keeps the response usable by both
		// classic Prometheus and the newer OpenMetrics format
		// (Grafana Alloy / OTel collectors prefer the latter).
		EnableOpenMetrics: true,
	})
}

// StatusFamily collapses an HTTP status code into a string label
// ("2xx" / "3xx" / "4xx" / "5xx"). Keeping families instead of raw
// codes is the single biggest cardinality saver — every distinct
// status would multiply the time-series count.
func StatusFamily(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
}
