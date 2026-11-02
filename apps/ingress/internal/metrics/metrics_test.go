// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
)

type nopRecorder struct{ n int }

func (r *nopRecorder) Record(proxy.Record) { r.n++ }

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestMetrics_RecorderAndRefresh(t *testing.T) {
	m := New("test")
	next := &nopRecorder{}
	rec := m.Recorder(next)
	rec.Record(proxy.Record{ServiceID: "svc-1", Label: "app", ClientIP: "203.0.113.7", Status: 200})
	rec.Record(proxy.Record{ServiceID: "svc-1", Label: "app", ClientIP: "203.0.113.8", Status: 429})
	m.OnRefresh(3, nil)
	m.OnRefresh(3, errors.New("down"))
	m.TelemetryDropped.Add(2)
	if next.n != 2 {
		t.Errorf("next recorder saw %d records, want 2", next.n)
	}
	body := scrape(t, m)
	for _, want := range []string{
		`bamboo_ingress_requests_total{code="2xx",service="svc-1"} 1`,
		`bamboo_ingress_requests_total{code="4xx",service="svc-1"} 1`,
		`bamboo_ingress_routes 3`,
		`bamboo_ingress_routes_refresh_failures_total 1`,
		`bamboo_ingress_telemetry_dropped_total 2`,
		`bamboo_ingress_build_info{version="test"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	// Bounded cardinality: no client address and no free-form label.
	for _, banned := range []string{"203.0.113", `service="app"`, "client"} {
		if strings.Contains(body, banned) {
			t.Errorf("metrics expose %q", banned)
		}
	}
}

func TestMetrics_StatusFamilyIsBounded(t *testing.T) {
	m := New("test")
	rec := m.Recorder(nil)
	for _, code := range []int{0, 99, 499, 600, 999} {
		rec.Record(proxy.Record{ServiceID: "svc-1", Status: code})
	}
	body := scrape(t, m)
	for _, want := range []string{`code="4xx",service="svc-1"} 1`, `code="other",service="svc-1"} 4`} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q in\n%s", want, body)
		}
	}
}
