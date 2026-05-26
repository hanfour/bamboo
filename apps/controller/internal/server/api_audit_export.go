// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// auditExportDefaultWindow is the lookback period when the caller
// omits `since`. 7 days mirrors the typical "what happened this
// week" incident-review use case; operators wanting more set
// `since` explicitly.
const auditExportDefaultWindow = 7 * 24 * time.Hour

// auditExportMaxRows caps the row count one export call can pull.
// Audit table is unbounded over time; without a cap a poorly-
// timed `?since=1970-01-01` from a curious admin would buffer the
// entire history into memory + the response stream. 50k rows is
// ~5MB CSV, comfortably streamable on the controller's existing
// 30s WriteTimeout. Beyond 50k callers should chunk by date range.
const auditExportMaxRows = 50_000

// routeAdminAuditExport serves GET /api/v1/admin/audit-log.csv —
// streams the calling admin's tenant audit_log as CSV for SOC 2
// incident review + GDPR Article 15 subject-access workflows.
//
// Auth: user-session JWT, is_admin must be true. Same gate as
// routeAdminRelays. Tenant scope is implicit from the admin
// user's row — there is no cross-tenant export today; super-admin
// instance-wide export is a follow-up.
//
// Query params:
//
//	since  RFC3339 timestamp lower bound (inclusive). Default
//	       now - 7d. Must be in the past.
//	until  RFC3339 timestamp upper bound (exclusive). Default
//	       now. Must be after `since`.
//	limit  Row cap, default 10000, hard max 50000. Rows beyond
//	       the cap are silently truncated — clients chunk by date
//	       range. We log the truncation server-side so an operator
//	       can spot a too-wide window.
//
// CSV shape: one row per audit event with header. Columns chosen
// for the SOC 2 + GDPR readers' canonical fields; `diff` is JSON
// passed through as a string column so reviewers can grep but
// don't need a JSONB-aware tool.
func (h *HTTPServer) routeAdminAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authn, err := h.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if authn == nil || authn.claims == nil {
		writeError(w, http.StatusUnauthorized, errors.New("admin auth required"))
		return
	}
	user, err := h.users.GetByID(r.Context(), authn.claims.UserID)
	if err != nil || user == nil || !user.IsAdmin {
		writeError(w, http.StatusForbidden, errors.New("admin only"))
		return
	}

	since, until, limit, perr := parseAuditExportParams(r.URL.Query())
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr)
		return
	}

	// Hand the streaming to the repo so we don't buffer up to 50k
	// rows in a Go slice before writing. The handler owns the CSV
	// header + framing; the repo owns the DB cursor.
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// RFC 6266 filename — fixed prefix + ISO date of the export's
	// upper bound (until) so a user downloading a historical window
	// gets a filename that matches the content, not "today". The
	// tenant slug is left out so it doesn't leak via the file's
	// metadata if forwarded.
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="audit-%s.csv"`,
		until.UTC().Format("2006-01-02"),
	))
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	if err := cw.Write(auditExportHeader); err != nil {
		// At this point we've already sent 200 + headers — the
		// best we can do is log and let the truncated body
		// surface as a CSV-parse error on the client.
		slog.Warn("audit export: write header", "err", err)
		return
	}

	written := 0
	streamErr := h.audits.StreamByTenantInRange(r.Context(), user.TenantID, since, until, limit, func(ev *repo.AuditEvent) error {
		if err := cw.Write(auditEventToCSVRow(ev)); err != nil {
			return err
		}
		written++
		return nil
	})
	cw.Flush()
	if err := cw.Error(); err != nil {
		slog.Warn("audit export: csv flush", "err", err)
	}
	if streamErr != nil {
		slog.Warn("audit export: stream", "err", streamErr, "tenant_id", user.TenantID, "written", written)
		return
	}
	// Only log when we hit the server-side hard cap — a user
	// passing a smaller `limit` and reaching it is their own choice,
	// not a truncation worth surfacing in ops logs.
	if written == auditExportMaxRows {
		slog.Info("audit export: hit row cap",
			"tenant_id", user.TenantID, "limit", auditExportMaxRows,
			"since", since, "until", until,
			"hint", "chunk by narrower date range to fetch the rest")
	}
}

// parseAuditExportParams normalises the three query params into
// the (since, until, limit) tuple StreamByTenantInRange consumes.
// Defaults documented on the route comment above. Returns a
// 400-suitable error string ready for writeError.
func parseAuditExportParams(q map[string][]string) (since, until time.Time, limit int, err error) {
	now := time.Now().UTC()
	until = now
	since = now.Add(-auditExportDefaultWindow)
	limit = 10_000

	if v := firstQuery(q, "since"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return time.Time{}, time.Time{}, 0, fmt.Errorf("invalid since=%q (want RFC3339)", v)
		}
		since = t.UTC()
	}
	if v := firstQuery(q, "until"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return time.Time{}, time.Time{}, 0, fmt.Errorf("invalid until=%q (want RFC3339)", v)
		}
		until = t.UTC()
	}
	if !since.Before(until) {
		return time.Time{}, time.Time{}, 0, errors.New("since must be strictly before until")
	}
	if v := firstQuery(q, "limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n <= 0 {
			return time.Time{}, time.Time{}, 0, fmt.Errorf("invalid limit=%q (want positive integer)", v)
		}
		if n > auditExportMaxRows {
			n = auditExportMaxRows
		}
		limit = n
	}
	return since, until, limit, nil
}

func firstQuery(q map[string][]string, key string) string {
	if vs, ok := q[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// auditExportHeader is the column order both the CSV writer here
// and the test consumer rely on. Kept package-scoped so the test
// file can assert against it without copying the list.
var auditExportHeader = []string{
	"occurred_at",
	"actor_type",
	"actor_email",
	"actor_id",
	"action",
	"resource_type",
	"resource_id",
	"ip_address",
	"user_agent",
	"diff",
}

func auditEventToCSVRow(ev *repo.AuditEvent) []string {
	actorID := ""
	if ev.ActorID != nil {
		actorID = ev.ActorID.String()
	}
	resourceID := ""
	if ev.ResourceID != nil {
		resourceID = ev.ResourceID.String()
	}
	ip := ""
	if ev.IPAddress != nil {
		ip = *ev.IPAddress
	}
	userAgent := ""
	if ev.UserAgent != nil {
		userAgent = *ev.UserAgent
	}
	diff := ""
	if len(ev.Diff) > 0 {
		// json.RawMessage is bytes verbatim; CSV writer will
		// quote on its own when the value contains commas /
		// quotes / newlines, so no further escape needed here.
		diff = string(ev.Diff)
	}
	// CSV / Formula Injection defence (OWASP) — see
	// sanitizeCSVField. Applied to every field that may carry
	// user-controllable input so an exported file opened in
	// Excel / LibreOffice / Google Sheets doesn't auto-execute
	// =cmd|'/c calc'!A1 -style payloads. occurred_at +
	// actor_type are server-generated formats / enums and don't
	// need sanitisation; UUIDs ditto.
	return []string{
		ev.OccurredAt.UTC().Format(time.RFC3339Nano),
		ev.ActorType,
		sanitizeCSVField(ev.ActorEmail),
		actorID,
		sanitizeCSVField(ev.Action),
		sanitizeCSVField(ev.ResourceType),
		resourceID,
		sanitizeCSVField(ip),
		sanitizeCSVField(userAgent),
		sanitizeCSVField(diff),
	}
}

// sanitizeCSVField defends against CSV / Formula Injection
// (OWASP) by neutralising leading characters that Excel,
// LibreOffice Calc, and Google Sheets interpret as formula
// opcodes. Prefixes the value with an apostrophe — the
// universal "treat as text" hint these tools all honour.
//
// Triggers on =, +, -, @ (formula leaders) plus TAB and CR
// (whitespace leaders that some parsers strip before re-
// evaluating the next char as a formula leader). The empty
// string passes through unchanged.
//
// Cost is a one-byte prefix on the rare malicious row. The
// alternative — refusing to export rows that look hostile —
// would silently drop legitimate audit data whose action /
// resource_type / diff happens to start with one of these
// characters, which is the wrong tradeoff for a compliance
// export.
func sanitizeCSVField(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
