// SPDX-License-Identifier: AGPL-3.0-or-later

// Package metrics is bamboo-ingress's Prometheus surface, served on the
// admin listener at /metrics.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the collectors. Labels are bounded: service is the
// service_id from the routes table (one per live service), code is the
// status family. Client addresses never become labels.
type Metrics struct {
	Registry         *prometheus.Registry
	Requests         *prometheus.CounterVec
	Duration         *prometheus.HistogramVec
	Routes           prometheus.Gauge
	RefreshFailures  prometheus.Counter
	LastRefresh      prometheus.Gauge
	TelemetryDropped prometheus.Counter
}

// New registers every collector on a fresh registry.
func New(version string) *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bamboo_ingress_requests_total", Help: "Routed requests by service id and status family.",
		}, []string{"service", "code"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "bamboo_ingress_request_duration_seconds", Help: "Request duration, including streams.",
			Buckets: []float64{0.05, 0.25, 1, 5, 30, 120, 600},
		}, []string{"service"}),
		Routes:           prometheus.NewGauge(prometheus.GaugeOpts{Name: "bamboo_ingress_routes", Help: "Routes in the current table."}),
		RefreshFailures:  prometheus.NewCounter(prometheus.CounterOpts{Name: "bamboo_ingress_routes_refresh_failures_total", Help: "Failed routes refreshes (last table kept)."}),
		LastRefresh:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "bamboo_ingress_routes_last_success_timestamp_seconds", Help: "Unix time of the last successful refresh."}),
		TelemetryDropped: prometheus.NewCounter(prometheus.CounterOpts{Name: "bamboo_ingress_telemetry_dropped_total", Help: "Telemetry records dropped (queue full or post failed)."}),
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "bamboo_ingress_build_info", Help: "Build version."}, []string{"version"})
	build.WithLabelValues(version).Set(1)
	m.Registry.MustRegister(m.Requests, m.Duration, m.Routes, m.RefreshFailures, m.LastRefresh, m.TelemetryDropped, build)
	return m
}

// Handler serves the registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// OnRefresh is a routes.PollerOptions.OnResult callback.
func (m *Metrics) OnRefresh(n int, err error) {
	m.Routes.Set(float64(n))
	if err != nil {
		m.RefreshFailures.Inc()
		return
	}
	m.LastRefresh.Set(float64(time.Now().Unix()))
}

// statusFamily maps a status to one of six values.
func statusFamily(code int) string {
	if code < 100 || code > 599 {
		return "other"
	}
	return strconv.Itoa(code/100) + "xx"
}

type chain struct {
	m    *Metrics
	next proxy.Recorder
}

func (c chain) Record(r proxy.Record) {
	c.m.Requests.WithLabelValues(r.ServiceID, statusFamily(r.Status)).Inc()
	c.m.Duration.WithLabelValues(r.ServiceID).Observe(r.Duration.Seconds())
	if c.next != nil {
		c.next.Record(r)
	}
}

// Recorder counts each record, then passes it to next (which may be nil).
func (m *Metrics) Recorder(next proxy.Recorder) proxy.Recorder { return chain{m: m, next: next} }
