// SPDX-License-Identifier: AGPL-3.0-or-later

package recommend

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// KindFlagAnomalous extends the Kind enum (declared in unused.go).
// Mirrors bamboov1.Recommendation_KIND_FLAG_ANOMALOUS.
const KindFlagAnomalous Kind = 4

// AnomalyFinding mirrors clickhouse.AnomalyFinding so the recommender
// stays driver-agnostic.
type AnomalyFinding struct {
	ID           uuid.UUID
	OccurredAt   time.Time
	GeneratedAt  time.Time
	Score        float32
	ModelVersion string
	EventSummary string // JSON blob with the salient signal fields
}

// Anomalies turns Tier-2 ML findings into Recommendation values. Unlike
// the Tier-1 kinds, the Diff is empty: anomalies are observations, not
// proposed config changes. The operator's job is to triage, not apply.
//
// minScore filters out low-confidence flags so the recommendation list
// does not become noise.
func Anomalies(findings []AnomalyFinding, minScore float32) []Recommendation {
	if len(findings) == 0 {
		return nil
	}
	out := make([]Recommendation, 0, len(findings))
	for _, f := range findings {
		if f.Score < minScore {
			continue
		}
		out = append(out, Recommendation{
			ID:   f.ID,
			Kind: KindFlagAnomalous,
			Summary: fmt.Sprintf(
				"Anomalous traffic flagged at %s (score %.2f, model %s).",
				f.OccurredAt.UTC().Format(time.RFC3339),
				f.Score,
				abbreviateModel(f.ModelVersion),
			),
			Diff: "",
			Evidence: []string{
				fmt.Sprintf("score=%.4f", f.Score),
				fmt.Sprintf("model=%s", f.ModelVersion),
				fmt.Sprintf("event=%s", f.EventSummary),
			},
			// Confidence == anomaly score, clamped to [0, 0.95]. We do not
			// claim certainty on a single ML output; operators decide.
			Confidence:  clampConfidence(f.Score),
			GeneratedAt: f.GeneratedAt,
		})
	}
	return out
}

func clampConfidence(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 0.95 {
		return 0.95
	}
	return v
}

// abbreviateModel keeps the summary readable when model versions get
// long (e.g. "isolation-forest-v1-2026-05-08T03:14:15Z").
func abbreviateModel(s string) string {
	const max = 32
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
