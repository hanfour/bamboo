// SPDX-License-Identifier: AGPL-3.0-or-later

package recommend_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/policy/recommend"
)

func TestAnomalies_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	findings := []recommend.AnomalyFinding{
		{
			ID:           uuid.New(),
			OccurredAt:   now.Add(-time.Hour),
			GeneratedAt:  now,
			Score:        0.82,
			ModelVersion: "isolation-forest-v1-2026-05-08",
			EventSummary: `{"event_type":"CONNECTION_ESTABLISHED","bytes_sent":50000000}`,
		},
	}
	recs := recommend.Anomalies(findings, 0.5)
	if len(recs) != 1 {
		t.Fatalf("recs = %d, want 1", len(recs))
	}
	r := recs[0]
	if r.Kind != recommend.KindFlagAnomalous {
		t.Errorf("kind = %v, want KindFlagAnomalous", r.Kind)
	}
	if r.Diff != "" {
		t.Errorf("Diff should be empty for anomaly recommendations, got %q", r.Diff)
	}
	if !strings.Contains(r.Summary, "0.82") {
		t.Errorf("summary missing score: %q", r.Summary)
	}
	if r.Confidence > 0.95 {
		t.Errorf("confidence not clamped: %v", r.Confidence)
	}
}

func TestAnomalies_FiltersBelowThreshold(t *testing.T) {
	findings := []recommend.AnomalyFinding{
		{ID: uuid.New(), Score: 0.3, ModelVersion: "v1", GeneratedAt: time.Now()},
		{ID: uuid.New(), Score: 0.6, ModelVersion: "v1", GeneratedAt: time.Now()},
		{ID: uuid.New(), Score: 0.9, ModelVersion: "v1", GeneratedAt: time.Now()},
	}
	recs := recommend.Anomalies(findings, 0.5)
	if len(recs) != 2 {
		t.Errorf("recs = %d, want 2 (0.3 below 0.5 threshold)", len(recs))
	}
}

func TestAnomalies_ConfidenceMatchesScore(t *testing.T) {
	findings := []recommend.AnomalyFinding{
		{ID: uuid.New(), Score: 0.42, ModelVersion: "v1", GeneratedAt: time.Now()},
	}
	recs := recommend.Anomalies(findings, 0.0)
	if recs[0].Confidence != 0.42 {
		t.Errorf("confidence = %v, want 0.42", recs[0].Confidence)
	}
}

func TestAnomalies_EmptyInput(t *testing.T) {
	if got := recommend.Anomalies(nil, 0.5); got != nil {
		t.Errorf("nil input returned %v", got)
	}
}

func TestAnomalies_AbbreviatesLongModelVersionInSummary(t *testing.T) {
	long := strings.Repeat("isolation-forest-", 5)
	findings := []recommend.AnomalyFinding{
		{ID: uuid.New(), Score: 0.7, ModelVersion: long, GeneratedAt: time.Now()},
	}
	recs := recommend.Anomalies(findings, 0.0)
	if !strings.Contains(recs[0].Summary, "…") {
		t.Errorf("expected abbreviated model version in summary: %q", recs[0].Summary)
	}
}
