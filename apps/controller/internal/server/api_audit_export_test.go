// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// TestParseAuditExportParams_Defaults pins the no-args case:
// since=now-7d, until=now, limit=10000. A regression here would
// either change the default window silently (surprising operator)
// or break the "just hit /audit-log.csv" common path.
func TestParseAuditExportParams_Defaults(t *testing.T) {
	since, until, limit, err := parseAuditExportParams(map[string][]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limit != 10_000 {
		t.Errorf("limit = %d, want 10000", limit)
	}
	// since within 7d±1m of now-7d; until within 1m of now.
	now := time.Now().UTC()
	wantSinceAbout := now.Add(-auditExportDefaultWindow)
	if d := wantSinceAbout.Sub(since).Abs(); d > time.Minute {
		t.Errorf("since drift = %v, want <1m around now-7d", d)
	}
	if d := now.Sub(until).Abs(); d > time.Minute {
		t.Errorf("until drift = %v, want <1m around now", d)
	}
}

// TestParseAuditExportParams_ExplicitWindow pins the operator
// override path: supplying since + until + limit yields exactly
// those values (no clamping below the max, no drift).
func TestParseAuditExportParams_ExplicitWindow(t *testing.T) {
	q := url.Values{}
	q.Set("since", "2026-05-01T00:00:00Z")
	q.Set("until", "2026-05-08T00:00:00Z")
	q.Set("limit", "500")
	since, until, limit, err := parseAuditExportParams(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !since.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %v", since)
	}
	if !until.Equal(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("until = %v", until)
	}
	if limit != 500 {
		t.Errorf("limit = %d, want 500", limit)
	}
}

// TestParseAuditExportParams_LimitClamp pins the
// auditExportMaxRows ceiling. An admin passing limit=999999
// (curious or copy-pasted from a different system) must get
// 50000 silently — the streaming budget assumes this cap is
// respected.
func TestParseAuditExportParams_LimitClamp(t *testing.T) {
	q := url.Values{}
	q.Set("limit", "999999")
	_, _, limit, err := parseAuditExportParams(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if limit != auditExportMaxRows {
		t.Errorf("limit = %d, want %d (clamped)", limit, auditExportMaxRows)
	}
}

// TestParseAuditExportParams_InvalidErrors pins each 4xx path:
// malformed since / until, reversed window, zero / negative
// limit. The handler returns the error to writeError → 400; the
// test exercises the parsing layer where the error text gets
// shaped.
func TestParseAuditExportParams_InvalidErrors(t *testing.T) {
	cases := []struct {
		name string
		q    url.Values
		want string // substring of err.Error
	}{
		{
			name: "since malformed",
			q:    url.Values{"since": {"yesterday"}},
			want: "invalid since",
		},
		{
			name: "until malformed",
			q:    url.Values{"until": {"tomorrow"}},
			want: "invalid until",
		},
		{
			name: "reversed window",
			q: url.Values{
				"since": {"2026-05-10T00:00:00Z"},
				"until": {"2026-05-01T00:00:00Z"},
			},
			want: "before until",
		},
		{
			name: "limit zero",
			q:    url.Values{"limit": {"0"}},
			want: "invalid limit",
		},
		{
			name: "limit negative",
			q:    url.Values{"limit": {"-1"}},
			want: "invalid limit",
		},
		{
			name: "limit non-numeric",
			q:    url.Values{"limit": {"many"}},
			want: "invalid limit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := parseAuditExportParams(tc.q)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestAuditEventToCSVRow pins the column→field mapping. A column
// reorder bug here would silently mis-attribute events to actors;
// future readers grep for the column index in their tools so
// stability matters.
func TestAuditEventToCSVRow(t *testing.T) {
	actorID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	resourceID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	ip := "203.0.113.5"
	ua := "Mozilla/5.0"
	ev := &repo.AuditEvent{
		OccurredAt:   time.Date(2026, 5, 15, 10, 30, 0, 0, time.UTC),
		ActorType:    "user",
		ActorEmail:   "alice@example.com",
		ActorID:      &actorID,
		Action:       "peer.register",
		ResourceType: "peer",
		ResourceID:   &resourceID,
		IPAddress:    &ip,
		UserAgent:    &ua,
		Diff:         json.RawMessage(`{"hostname":"new-laptop"}`),
	}
	row := auditEventToCSVRow(ev)
	if len(row) != len(auditExportHeader) {
		t.Fatalf("row length = %d, want %d (matches header)", len(row), len(auditExportHeader))
	}
	idx := func(col string) int {
		for i, c := range auditExportHeader {
			if c == col {
				return i
			}
		}
		t.Fatalf("column %q not in header", col)
		return -1
	}
	if row[idx("occurred_at")] != "2026-05-15T10:30:00Z" {
		t.Errorf("occurred_at = %q", row[idx("occurred_at")])
	}
	if row[idx("actor_email")] != "alice@example.com" {
		t.Errorf("actor_email = %q", row[idx("actor_email")])
	}
	if row[idx("actor_id")] != actorID.String() {
		t.Errorf("actor_id = %q", row[idx("actor_id")])
	}
	if row[idx("action")] != "peer.register" {
		t.Errorf("action = %q", row[idx("action")])
	}
	if row[idx("ip_address")] != ip {
		t.Errorf("ip_address = %q", row[idx("ip_address")])
	}
	if row[idx("diff")] != `{"hostname":"new-laptop"}` {
		t.Errorf("diff = %q", row[idx("diff")])
	}
}

// TestAuditEventToCSVRow_NilPointers pins the safe-handling of
// system-actor / no-resource / no-ip rows. Hardware bootstrap +
// migration events have ActorID=nil; a panic here would crash the
// whole export on the first such row.
func TestAuditEventToCSVRow_NilPointers(t *testing.T) {
	ev := &repo.AuditEvent{
		OccurredAt:   time.Date(2026, 5, 15, 10, 30, 0, 0, time.UTC),
		ActorType:    "system",
		Action:       "invitation.expire",
		ResourceType: "invitation",
		// ActorID, ResourceID, IPAddress, UserAgent all nil; Diff empty
	}
	row := auditEventToCSVRow(ev)
	idx := func(col string) int {
		for i, c := range auditExportHeader {
			if c == col {
				return i
			}
		}
		return -1
	}
	if row[idx("actor_id")] != "" {
		t.Errorf("actor_id should be empty for system actor, got %q", row[idx("actor_id")])
	}
	if row[idx("resource_id")] != "" {
		t.Errorf("resource_id empty fallback failed: %q", row[idx("resource_id")])
	}
	if row[idx("ip_address")] != "" {
		t.Errorf("ip_address empty fallback failed: %q", row[idx("ip_address")])
	}
	if row[idx("diff")] != "" {
		t.Errorf("diff empty fallback failed: %q", row[idx("diff")])
	}
}
