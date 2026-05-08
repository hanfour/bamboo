// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
	"github.com/hanfour/bamboo/apps/controller/internal/policy/recommend"
)

// routeAPI dispatches /api/v1/* to read-only JSON endpoints.
//
// Tenant resolution: header X-Tenant-Slug (default "default"). Phase 1
// has no AuthN here; production will require a session JWT and the
// tenant resolves from claims rather than a header.
func (h *HTTPServer) routeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenant, err := h.resolveTenant(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	switch r.URL.Path {
	case "/api/v1/overview":
		h.apiOverview(w, r, tenant)
	case "/api/v1/peers":
		h.apiPeers(w, r, tenant)
	case "/api/v1/policy":
		h.apiPolicy(w, r, tenant)
	case "/api/v1/recommendations":
		h.apiRecommendations(w, r, tenant)
	default:
		http.NotFound(w, r)
	}
}

// resolveTenant reads X-Tenant-Slug, defaulting to "default", and
// auto-creates the tenant on first sight (mirrors the gRPC handlers).
func (h *HTTPServer) resolveTenant(r *http.Request) (*repo.Tenant, error) {
	slug := r.Header.Get("X-Tenant-Slug")
	if slug == "" {
		slug = "default"
	}
	return h.tenants.GetOrCreate(r.Context(), slug, "Default Tenant", "100.64.0.0/24")
}

// apiPeerJSON is the wire shape for the peers endpoint.
type apiPeerJSON struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenantId"`
	Hostname      string     `json:"hostname"`
	IP            string     `json:"ip"`
	Tags          []string   `json:"tags"`
	OS            string     `json:"os"`
	ClientVersion string     `json:"clientVersion"`
	Status        string     `json:"status"`
	LastSeenAt    *time.Time `json:"lastSeenAt,omitempty"`
}

func (h *HTTPServer) apiPeers(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	peers, err := h.peers.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiPeerJSON, 0, len(peers))
	for _, p := range peers {
		out = append(out, apiPeerJSON{
			ID:            p.ID.String(),
			TenantID:      p.TenantID.String(),
			Hostname:      p.Hostname,
			IP:            p.IP,
			Tags:          []string{}, // populated once peer_tags wiring lands
			OS:            p.OS,
			ClientVersion: p.ClientVersion,
			Status:        p.Status,
			LastSeenAt:    p.LastSeenAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": out})
}

type apiACLRuleJSON struct {
	ID           string   `json:"id"`
	Action       string   `json:"action"`
	Description  string   `json:"description,omitempty"`
	Sources      []string `json:"sources"`
	Destinations []string `json:"destinations"`
}

type apiPolicyJSON struct {
	TenantID  string           `json:"tenantId"`
	Revision  int64            `json:"revision"`
	HCLSource string           `json:"hclSource"`
	UpdatedAt *time.Time       `json:"updatedAt,omitempty"`
	Rules     []apiACLRuleJSON `json:"rules"`
}

func (h *HTTPServer) apiPolicy(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	rec, err := h.policies.Get(r.Context(), tenant.ID)
	if errors.Is(err, repo.ErrNotFound) {
		writeJSON(w, http.StatusOK, apiPolicyJSON{
			TenantID: tenant.ID.String(),
			Revision: 0,
			Rules:    []apiACLRuleJSON{},
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	parsed, _ := policy.Parse("policy.hcl", rec.HCLSource)
	rules := []apiACLRuleJSON{}
	if parsed != nil {
		for _, ru := range parsed.Rules {
			rules = append(rules, apiACLRuleJSON{
				ID:           ru.ID,
				Action:       ru.Action.String(),
				Description:  ru.Description,
				Sources:      formatPolicySources(ru.Sources),
				Destinations: formatPolicyDestinations(ru.Destinations),
			})
		}
	}
	writeJSON(w, http.StatusOK, apiPolicyJSON{
		TenantID:  rec.TenantID.String(),
		Revision:  rec.Revision,
		HCLSource: rec.HCLSource,
		UpdatedAt: &rec.UpdatedAt,
		Rules:     rules,
	})
}

type apiRecommendationJSON struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	Diff        string    `json:"diff"`
	Evidence    []string  `json:"evidence"`
	Confidence  float32   `json:"confidence"`
	GeneratedAt time.Time `json:"generatedAt"`
}

func (h *HTTPServer) apiRecommendations(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	rec, err := h.policies.Get(r.Context(), tenant.ID)
	var parsed *policy.Policy
	switch {
	case errors.Is(err, repo.ErrNotFound):
		parsed = &policy.Policy{}
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	default:
		parsed, err = policy.Parse("policy.hcl", rec.HCLSource)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	since := time.Now().Add(-30 * 24 * time.Hour)
	hits, _ := h.traces.RuleHitCounts(r.Context(), tenant.ID, since)
	chObs, _ := h.traces.RuleObservations(r.Context(), tenant.ID, since)
	obs := make(map[string]*recommend.RuleObservation, len(chObs))
	for id, o := range chObs {
		obs[id] = &recommend.RuleObservation{
			RuleID:    o.RuleID,
			Ports:     o.Ports,
			TotalHits: o.TotalHits,
		}
	}
	chFlows, _ := h.traces.TopDeniedFlows(r.Context(), tenant.ID, since, 10, 5)
	flows := make([]recommend.DeniedFlow, len(chFlows))
	for i, f := range chFlows {
		flows[i] = recommend.DeniedFlow{
			Source:      f.Source,
			Destination: f.Destination,
			Port:        f.Port,
			Hits:        f.Hits,
		}
	}
	chFindings, _ := h.anomalies.RecentByTenant(r.Context(), tenant.ID, time.Now().Add(-24*time.Hour), 20)
	findings := make([]recommend.AnomalyFinding, len(chFindings))
	for i, f := range chFindings {
		findings[i] = recommend.AnomalyFinding{
			ID:           f.ID,
			OccurredAt:   f.OccurredAt,
			GeneratedAt:  f.GeneratedAt,
			Score:        f.Score,
			ModelVersion: f.ModelVersion,
			EventSummary: f.EventSummary,
		}
	}

	recs := recommend.UnusedRules(parsed, hits, since)
	recs = append(recs, recommend.OverPrivilegedRules(parsed, obs, since)...)
	recs = append(recs, recommend.BroadenNeeded(parsed, flows, since)...)
	recs = append(recs, recommend.Anomalies(findings, 0.6)...)

	out := make([]apiRecommendationJSON, 0, len(recs))
	for _, x := range recs {
		out = append(out, apiRecommendationJSON{
			ID:          x.ID.String(),
			Kind:        kindString(x.Kind),
			Summary:     x.Summary,
			Diff:        x.Diff,
			Evidence:    x.Evidence,
			Confidence:  x.Confidence,
			GeneratedAt: x.GeneratedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"recommendations": out})
}

type apiOverviewJSON struct {
	TenantID       string `json:"tenantId"`
	TotalPeers     int    `json:"totalPeers"`
	OnlinePeers    int    `json:"onlinePeers"`
	OfflinePeers   int    `json:"offlinePeers"`
	PolicyRevision int64  `json:"policyRevision"`
	RecommendCount int    `json:"recommendationCount"`
}

func (h *HTTPServer) apiOverview(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	peers, err := h.peers.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	online := 0
	for _, p := range peers {
		if p.Status == "online" {
			online++
		}
	}

	revision := int64(0)
	rec, err := h.policies.Get(r.Context(), tenant.ID)
	if err == nil {
		revision = rec.Revision
	}

	recommendCount := countRecommendations(r.Context(), h, tenant)

	writeJSON(w, http.StatusOK, apiOverviewJSON{
		TenantID:       tenant.ID.String(),
		TotalPeers:     len(peers),
		OnlinePeers:    online,
		OfflinePeers:   len(peers) - online,
		PolicyRevision: revision,
		RecommendCount: recommendCount,
	})
}

func countRecommendations(ctx context.Context, h *HTTPServer, tenant *repo.Tenant) int {
	rec, err := h.policies.Get(ctx, tenant.ID)
	if err != nil {
		return 0
	}
	parsed, err := policy.Parse("policy.hcl", rec.HCLSource)
	if err != nil {
		return 0
	}
	since := time.Now().Add(-30 * 24 * time.Hour)
	hits, _ := h.traces.RuleHitCounts(ctx, tenant.ID, since)
	chObs, _ := h.traces.RuleObservations(ctx, tenant.ID, since)
	obs := make(map[string]*recommend.RuleObservation, len(chObs))
	for id, o := range chObs {
		obs[id] = &recommend.RuleObservation{
			RuleID:    o.RuleID,
			Ports:     o.Ports,
			TotalHits: o.TotalHits,
		}
	}
	chFlows, _ := h.traces.TopDeniedFlows(ctx, tenant.ID, since, 10, 5)
	flows := make([]recommend.DeniedFlow, len(chFlows))
	for i, f := range chFlows {
		flows[i] = recommend.DeniedFlow{
			Source: f.Source, Destination: f.Destination, Port: f.Port, Hits: f.Hits,
		}
	}
	chFindings, _ := h.anomalies.RecentByTenant(ctx, tenant.ID, time.Now().Add(-24*time.Hour), 20)
	findings := make([]recommend.AnomalyFinding, len(chFindings))
	for i, f := range chFindings {
		findings[i] = recommend.AnomalyFinding{
			ID:           f.ID,
			OccurredAt:   f.OccurredAt,
			GeneratedAt:  f.GeneratedAt,
			Score:        f.Score,
			ModelVersion: f.ModelVersion,
			EventSummary: f.EventSummary,
		}
	}
	return len(recommend.UnusedRules(parsed, hits, since)) +
		len(recommend.OverPrivilegedRules(parsed, obs, since)) +
		len(recommend.BroadenNeeded(parsed, flows, since)) +
		len(recommend.Anomalies(findings, 0.6))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// formatPolicySources / Destinations duplicate the small renderer the
// gRPC handler uses; keeping them in this file avoids cross-package
// imports and keeps the JSON wire shape obvious.
func formatPolicySources(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatPolicyMatcher(m))
	}
	return out
}

func formatPolicyDestinations(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatPolicyMatcher(m)+":"+m.Ports.String())
	}
	return out
}

func formatPolicyMatcher(m policy.Matcher) string {
	switch m.Kind {
	case policy.MatcherWildcard:
		return "*"
	case policy.MatcherTag:
		return "tag:" + m.Name
	case policy.MatcherGroup:
		return "group:" + m.Name
	case policy.MatcherUser:
		return "user:" + m.Name
	case policy.MatcherCIDR:
		return "cidr:" + m.CIDR.String()
	default:
		return "?"
	}
}

func kindString(k recommend.Kind) string {
	switch k {
	case recommend.KindRemoveUnusedRule:
		return "REMOVE_UNUSED_RULE"
	case recommend.KindTightenOverPrivileged:
		return "TIGHTEN_OVERPRIVILEGED"
	case recommend.KindBroadenNeeded:
		return "BROADEN_NEEDED"
	case recommend.KindFlagAnomalous:
		return "FLAG_ANOMALOUS"
	default:
		return "UNKNOWN"
	}
}

// _ keeps uuid imported (used elsewhere if api.go grows).
var _ uuid.UUID
