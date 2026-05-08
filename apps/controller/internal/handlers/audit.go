// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// auditLog inserts an audit event, logging and swallowing any error so
// that audit-write failures never fail the user-facing operation.
//
// Use this from every state-changing handler. Heartbeat is intentionally
// not audited (high frequency, low value).
func auditLog(ctx context.Context, audits *repo.AuditLogs, e *repo.AuditEvent) {
	if audits == nil {
		return
	}
	if err := audits.Insert(ctx, e); err != nil {
		slog.Warn("audit insert failed",
			"action", e.Action,
			"resource_type", e.ResourceType,
			"err", err)
	}
}

// marshalDiff returns a JSON encoding of v, or nil on marshal failure.
func marshalDiff(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
