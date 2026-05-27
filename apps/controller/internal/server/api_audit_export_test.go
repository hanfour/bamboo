// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestSanitizeCSVField pins the formula-injection neutralisation
// (OWASP CSV Injection). A regression here would re-open the
// auto-execution vector on Excel / LibreOffice / Sheets — the
// whole point of this defence is that exported audit CSVs are
// the payload for SOC 2 / GDPR reviewers, not just internal
// inspection.
func TestSanitizeCSVField(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"normal text":        "normal text",
		"=cmd|'/c calc'!A1":  "'=cmd|'/c calc'!A1",
		"+SUM(A1:A99)":       "'+SUM(A1:A99)",
		"-2+3":               "'-2+3",
		"@SUM(1+9)":          "'@SUM(1+9)",
		"\t=evil":            "'\t=evil",
		"\r=evil":            "'\r=evil",
		"alice@example.com":  "alice@example.com",
		"peer.register":      "peer.register",
		`{"hostname":"x"}`:   `{"hostname":"x"}`,
		"name with = inside": "name with = inside",
	}
	for in, want := range cases {
		if got := sanitizeCSVField(in); got != want {
			t.Errorf("sanitizeCSVField(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAuditEventToCSVRow_FormulaSanitization confirms the
// sanitisation is wired through the row builder for every
// user-controllable column. A field-position regression here
// would silently re-expose the formula-injection vector even if
// the helper itself stays correct.
func TestAuditEventToCSVRow_FormulaSanitization(t *testing.T) {
	hostile := "=cmd|'/c calc'!A0"
	ua := "=BAD"
	ip := "-1"
	ev := &repo.AuditEvent{
		OccurredAt:   time.Date(2026, 5, 15, 10, 30, 0, 0, time.UTC),
		ActorType:    "user",
		ActorEmail:   hostile,
		Action:       hostile,
		ResourceType: hostile,
		IPAddress:    &ip,
		UserAgent:    &ua,
		Diff:         json.RawMessage(`=danger`),
	}
	row := auditEventToCSVRow(ev)
	idx := func(col string) int {
		for i, c := range auditExportHeader {
			if c == col {
				return i
			}
		}
		t.Fatalf("column %q not in header", col)
		return -1
	}
	want := "'" + hostile
	for _, col := range []string{"actor_email", "action", "resource_type"} {
		if got := row[idx(col)]; got != want {
			t.Errorf("%s = %q, want %q (formula-prefixed)", col, got, want)
		}
	}
	if got := row[idx("ip_address")]; got != "'-1" {
		t.Errorf("ip_address = %q, want %q", got, "'-1")
	}
	if got := row[idx("user_agent")]; got != "'=BAD" {
		t.Errorf("user_agent = %q, want %q", got, "'=BAD")
	}
	if got := row[idx("diff")]; got != "'=danger" {
		t.Errorf("diff = %q, want %q", got, "'=danger")
	}
}

// TestRouteAdminAuditExport_MethodNotAllowed pins the 405 path.
// The handler short-circuits non-GET before touching auth, so
// this works without a configured server — and a regression
// (e.g. accidentally accepting POST) would change the API
// contract the Next proxy + clients depend on.
func TestRouteAdminAuditExport_MethodNotAllowed(t *testing.T) {
	h := &HTTPServer{
		secret: []byte("test-secret-with-at-least-32-bytes-padding"),
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/v1/admin/audit-log.csv", nil)
			rec := httptest.NewRecorder()
			h.routeAdminAuditExport(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("got %d, want 405 (%s)", rec.Code, method)
			}
		})
	}
}

// TestRouteAdminAuditExport_RequiresAuth pins the auth gate.
// An unauthenticated GET must surface as 401 BEFORE any DB
// lookup, mirroring routeAdminUsers / routeAdminRelays. The
// same shape — empty HTTPServer + no cookie — is sufficient
// because authenticate fails before users.GetByID is called.
func TestRouteAdminAuditExport_RequiresAuth(t *testing.T) {
	h := &HTTPServer{
		secret: []byte("test-secret-with-at-least-32-bytes-padding"),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/audit-log.csv", nil)
	rec := httptest.NewRecorder()
	h.routeAdminAuditExport(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}
