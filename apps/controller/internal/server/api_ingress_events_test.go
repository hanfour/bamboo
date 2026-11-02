// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func TestIngressEventRow(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	claims := &auth.IngressClaims{TenantID: uuid.New(), PeerID: uuid.New()}
	sid, target := uuid.New(), uuid.New()
	peers := map[uuid.UUID]uuid.UUID{sid: target}
	good := func() ingressEventJSON {
		return ingressEventJSON{ServiceID: sid.String(), ClientIP: "203.0.113.7", Status: 200,
			BytesIn: 10, BytesOut: 20, DurationMs: 5, OccurredAt: now.Add(-time.Minute)}
	}

	row, ok := ingressEventRow(good(), claims, peers, now)
	if !ok {
		t.Fatal("valid event dropped")
	}
	want := good()
	if row.TenantID != claims.TenantID || row.IngressPeerID != claims.PeerID || row.TargetPeerID != target ||
		row.ServiceID != sid || row.ClientIP != "203.0.113.7" || row.Status != 200 ||
		row.BytesIn != 10 || row.BytesOut != 20 || row.DurationMs != 5 || !row.OccurredAt.Equal(want.OccurredAt) {
		t.Errorf("row = %+v", row)
	}

	drop := map[string]func(*ingressEventJSON){
		"bad uuid":        func(e *ingressEventJSON) { e.ServiceID = "nope" },
		"foreign service": func(e *ingressEventJSON) { e.ServiceID = uuid.NewString() },
		"status 99":       func(e *ingressEventJSON) { e.Status = 99 },
		"status 600":      func(e *ingressEventJSON) { e.Status = 600 },
		"negative in":     func(e *ingressEventJSON) { e.BytesIn = -1 },
		"negative out":    func(e *ingressEventJSON) { e.BytesOut = -1 },
		"negative dur":    func(e *ingressEventJSON) { e.DurationMs = -1 },
		"too old":         func(e *ingressEventJSON) { e.OccurredAt = now.Add(-maxIngressEventAge - time.Second) },
		"future":          func(e *ingressEventJSON) { e.OccurredAt = now.Add(maxIngressEventSkew + time.Second) },
	}
	for name, mut := range drop {
		e := good()
		mut(&e)
		if _, ok := ingressEventRow(e, claims, peers, now); ok {
			t.Errorf("%s: kept, want dropped", name)
		}
	}

	adjust := []struct {
		name  string
		mut   func(*ingressEventJSON)
		check func(r ingressRowView) bool
	}{
		{"zero time is now", func(e *ingressEventJSON) { e.OccurredAt = time.Time{} }, func(r ingressRowView) bool { return r.at.Equal(now) }},
		{"bad ip blanked", func(e *ingressEventJSON) { e.ClientIP = "not-an-ip" }, func(r ingressRowView) bool { return r.ip == "" }},
		{"ipv6 canonical, zone stripped", func(e *ingressEventJSON) { e.ClientIP = "2001:DB8::1%eth0" }, func(r ingressRowView) bool { return r.ip == "2001:db8::1" }},
		{"ipv4-mapped unmapped", func(e *ingressEventJSON) { e.ClientIP = "::ffff:203.0.113.9" }, func(r ingressRowView) bool { return r.ip == "203.0.113.9" }},
		{"duration clamped", func(e *ingressEventJSON) { e.DurationMs = math.MaxInt64 }, func(r ingressRowView) bool { return r.dur == math.MaxUint32 }},
	}
	for _, c := range adjust {
		e := good()
		c.mut(&e)
		r, ok := ingressEventRow(e, claims, peers, now)
		if !ok || !c.check(ingressRowView{at: r.OccurredAt, ip: r.ClientIP, dur: r.DurationMs}) {
			t.Errorf("%s: ok=%v row=%+v", c.name, ok, r)
		}
	}
}

type ingressRowView struct {
	at  time.Time
	ip  string
	dur uint32
}
