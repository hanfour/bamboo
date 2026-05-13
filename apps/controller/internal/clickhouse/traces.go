// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EvaluationTrace is one row in evaluation_traces.
type EvaluationTrace struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	OccurredAt    time.Time
	Source        string
	Destination   string
	Port          uint16
	Protocol      string
	Action        string
	MatchedRuleID string
}

// Traces is the writer / reader for evaluation_traces.
type Traces struct {
	c    *Conn
	skip sync.Once
	logs once
}

// NewTraces wraps a Conn (which may be nil) for the table.
func NewTraces(c *Conn) *Traces {
	return &Traces{c: c}
}

// Insert writes one trace. If the connection is not configured, the
// write is dropped after a single warning per process.
func (t *Traces) Insert(ctx context.Context, e *EvaluationTrace) error {
	if !t.c.IsConfigured() {
		t.skip.Do(func() { t.logs.do("evaluation_traces.insert") })
		return nil
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if e.Protocol == "" {
		e.Protocol = "tcp"
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return t.c.driver().Exec(ctx, `
		INSERT INTO evaluation_traces (
		    id, tenant_id, occurred_at, source, destination,
		    port, protocol, action, matched_rule_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.ID, e.TenantID, e.OccurredAt, e.Source, e.Destination,
		e.Port, e.Protocol, e.Action, e.MatchedRuleID,
	)
}

// RuleHitCounts returns the count of allow/deny hits per matched_rule_id
// for tenant since `since`. Rules with zero hits do not appear in the
// result; the caller cross-references with the policy to find unused
// rules.
func (t *Traces) RuleHitCounts(ctx context.Context, tenantID uuid.UUID, since time.Time) (map[string]uint64, error) {
	out := make(map[string]uint64)
	if !t.c.IsConfigured() {
		return out, nil
	}
	rows, err := t.c.driver().Query(ctx, `
		SELECT matched_rule_id, count() AS hits
		FROM evaluation_traces
		WHERE tenant_id = ?
		  AND occurred_at >= ?
		  AND matched_rule_id != ''
		GROUP BY matched_rule_id
	`, tenantID, since)
	if err != nil {
		return nil, fmt.Errorf("query rule hits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var hits uint64
		if err := rows.Scan(&id, &hits); err != nil {
			return nil, err
		}
		out[id] = hits
	}
	return out, rows.Err()
}

// DeniedFlow captures one (source, destination, port) triple that was
// repeatedly default-denied (matched_rule_id = ”). The recommender
// uses these to surface "broaden-needed" suggestions: traffic the
// operator may have intended to allow.
type DeniedFlow struct {
	Source      string
	Destination string
	Port        uint16
	Hits        uint64
}

// TopDeniedFlows returns the highest-frequency default-deny triples
// for a tenant since `since`. minHits filters one-off attempts; limit
// caps the result count so a noisy day cannot drown the operator.
func (t *Traces) TopDeniedFlows(ctx context.Context, tenantID uuid.UUID, since time.Time, minHits uint64, limit int) ([]DeniedFlow, error) {
	if !t.c.IsConfigured() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := t.c.driver().Query(ctx, `
		SELECT source, destination, port, count() AS hits
		FROM evaluation_traces
		WHERE tenant_id = ?
		  AND occurred_at >= ?
		  AND action = 'deny'
		  AND matched_rule_id = ''
		GROUP BY source, destination, port
		HAVING hits >= ?
		ORDER BY hits DESC
		LIMIT ?
	`, tenantID, since, minHits, uint64(limit))
	if err != nil {
		return nil, fmt.Errorf("query denied flows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DeniedFlow
	for rows.Next() {
		var f DeniedFlow
		if err := rows.Scan(&f.Source, &f.Destination, &f.Port, &f.Hits); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RuleObservation captures, per matched_rule_id, the histogram of
// destination ports observed in the action="allow" path. Used by the
// over-privileged-rule recommender to suggest tightening wildcard
// destination port specs to the actually-used ports.
type RuleObservation struct {
	RuleID    string
	Ports     map[uint16]uint64
	TotalHits uint64
}

// RuleObservations groups allow-action evaluation traces by rule + port.
// Returns nil-safe empty result when ClickHouse is not configured.
func (t *Traces) RuleObservations(ctx context.Context, tenantID uuid.UUID, since time.Time) (map[string]*RuleObservation, error) {
	out := make(map[string]*RuleObservation)
	if !t.c.IsConfigured() {
		return out, nil
	}
	rows, err := t.c.driver().Query(ctx, `
		SELECT matched_rule_id, port, count() AS hits
		FROM evaluation_traces
		WHERE tenant_id = ?
		  AND occurred_at >= ?
		  AND matched_rule_id != ''
		  AND action = 'allow'
		GROUP BY matched_rule_id, port
	`, tenantID, since)
	if err != nil {
		return nil, fmt.Errorf("query rule observations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			ruleID string
			port   uint16
			hits   uint64
		)
		if err := rows.Scan(&ruleID, &port, &hits); err != nil {
			return nil, err
		}
		o, ok := out[ruleID]
		if !ok {
			o = &RuleObservation{RuleID: ruleID, Ports: map[uint16]uint64{}}
			out[ruleID] = o
		}
		o.Ports[port] = hits
		o.TotalHits += hits
	}
	return out, rows.Err()
}

// ListRecent returns the most-recent evaluation traces for a tenant,
// newest first, capped by limit. Used by the admin /api/v1/logs
// endpoint to render a connection-event timeline (Tailscale parity
// for /admin/logs). When ClickHouse is unconfigured the method
// returns an empty slice + nil error — the UI treats that as "no
// events yet" rather than hard-erroring.
func (t *Traces) ListRecent(ctx context.Context, tenantID uuid.UUID, since time.Time, limit int) ([]EvaluationTrace, error) {
	if !t.c.IsConfigured() {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := t.c.driver().Query(ctx, `
		SELECT id, tenant_id, occurred_at, source, destination,
		       port, protocol, action, matched_rule_id
		FROM evaluation_traces
		WHERE tenant_id = ?
		  AND occurred_at >= ?
		ORDER BY occurred_at DESC
		LIMIT ?
	`, tenantID, since, uint64(limit))
	if err != nil {
		return nil, fmt.Errorf("query recent traces: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []EvaluationTrace
	for rows.Next() {
		var e EvaluationTrace
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.OccurredAt, &e.Source, &e.Destination,
			&e.Port, &e.Protocol, &e.Action, &e.MatchedRuleID,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
