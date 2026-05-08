// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AnomalyFinding is a single Tier-2 ML output: an event the model
// flagged as out-of-distribution, with a summary the controller can
// surface as a recommendation.
type AnomalyFinding struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	OccurredAt   time.Time
	GeneratedAt  time.Time
	Score        float32
	ModelVersion string
	EventID      uuid.UUID
	EventSummary string // JSON; opaque to the controller
}

// Anomalies is the read interface over anomaly_findings. The Go
// controller consumes findings the bamboo-ai Python module wrote.
type Anomalies struct {
	c *Conn
}

// NewAnomalies constructs the reader. ch may be nil (degraded mode).
func NewAnomalies(c *Conn) *Anomalies {
	return &Anomalies{c: c}
}

// RecentByTenant returns findings generated since `since` for tenant.
// Newest first; capped at limit.
func (a *Anomalies) RecentByTenant(ctx context.Context, tenantID uuid.UUID, since time.Time, limit int) ([]AnomalyFinding, error) {
	if !a.c.IsConfigured() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := a.c.driver().Query(ctx, `
		SELECT id, tenant_id, occurred_at, generated_at, score,
		       model_version, event_id, event_summary
		FROM anomaly_findings
		WHERE tenant_id = ?
		  AND generated_at >= ?
		ORDER BY generated_at DESC, score DESC
		LIMIT ?
	`, tenantID, since, uint64(limit))
	if err != nil {
		return nil, fmt.Errorf("query anomaly_findings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AnomalyFinding
	for rows.Next() {
		var f AnomalyFinding
		if err := rows.Scan(
			&f.ID, &f.TenantID, &f.OccurredAt, &f.GeneratedAt, &f.Score,
			&f.ModelVersion, &f.EventID, &f.EventSummary,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
