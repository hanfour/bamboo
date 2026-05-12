// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
	"github.com/hanfour/bamboo/apps/controller/internal/policy/recommend"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"github.com/hashicorp/hcl/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PolicyHandler implements bamboov1.PolicyServiceServer.
type PolicyHandler struct {
	bamboov1.UnimplementedPolicyServiceServer

	tenants   *repo.Tenants
	policies  *repo.Policies
	audits    *repo.AuditLogs
	traces    *clickhouse.Traces
	anomalies *clickhouse.Anomalies
	bus       *events.Bus
}

// NewPolicyHandler constructs a PolicyHandler with required repositories.
// ch may be nil; the handler degrades to no-op telemetry writes.
// bus may be nil; PolicyChanged events simply aren't published in that
// case (clients still pick up changes on the next heartbeat).
func NewPolicyHandler(pool *db.Pool, ch *clickhouse.Conn, bus *events.Bus) *PolicyHandler {
	return &PolicyHandler{
		tenants:   repo.NewTenants(pool),
		policies:  repo.NewPolicies(pool),
		audits:    repo.NewAuditLogs(pool),
		traces:    clickhouse.NewTraces(ch),
		anomalies: clickhouse.NewAnomalies(ch),
		bus:       bus,
	}
}

// GetPolicy returns the current policy for the calling tenant. If no
// policy has been written yet, returns an empty policy at revision 0.
func (h *PolicyHandler) GetPolicy(ctx context.Context, _ *bamboov1.GetPolicyRequest) (*bamboov1.GetPolicyResponse, error) {
	tenant, err := h.resolveTenant(ctx)
	if err != nil {
		return nil, err
	}

	rec, err := h.policies.Get(ctx, tenant.ID)
	if errors.Is(err, repo.ErrNotFound) {
		return &bamboov1.GetPolicyResponse{Policy: &bamboov1.Policy{
			TenantId: tenant.ID.String(),
			Revision: 0,
		}}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load policy: %v", err)
	}

	parsed, _ := policy.Parse("policy.hcl", rec.HCLSource)
	return &bamboov1.GetPolicyResponse{Policy: toProtoPolicy(rec, parsed)}, nil
}

// PutPolicy validates and persists a new policy revision.
func (h *PolicyHandler) PutPolicy(ctx context.Context, req *bamboov1.PutPolicyRequest) (*bamboov1.PutPolicyResponse, error) {
	tenant, err := h.resolveTenant(ctx)
	if err != nil {
		return nil, err
	}

	parsed, parseErr := policy.Parse("policy.hcl", req.GetHclSource())
	if parseErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid policy: %v", parseErr)
	}

	rec, err := h.policies.Put(ctx, tenant.ID, req.GetHclSource(), nil, req.GetExpectedRevision())
	if errors.Is(err, repo.ErrRevisionMismatch) {
		return nil, status.Error(codes.FailedPrecondition, "expected_revision does not match current")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "put policy: %v", err)
	}

	auditLog(ctx, h.audits, &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "policy.update",
		ResourceType: "policy",
		Diff: marshalDiff(map[string]any{
			"revision":  rec.Revision,
			"rules":     len(parsed.Rules),
			"hcl_bytes": len(rec.HCLSource),
		}),
	})

	if h.bus != nil {
		h.bus.Publish(tenant.ID, &bamboov1.WatchPeersEvent{
			Event: &bamboov1.WatchPeersEvent_PolicyChanged{
				PolicyChanged: &bamboov1.PolicyChanged{PolicyRevision: rec.Revision},
			},
		})
	}

	return &bamboov1.PutPolicyResponse{Policy: toProtoPolicy(rec, parsed)}, nil
}

// ValidatePolicy parses a candidate policy without persisting it. Returns
// the diagnostic list when invalid, or valid=true when clean.
func (h *PolicyHandler) ValidatePolicy(_ context.Context, req *bamboov1.ValidatePolicyRequest) (*bamboov1.ValidatePolicyResponse, error) {
	if _, err := policy.Parse("policy.hcl", req.GetHclSource()); err != nil {
		return &bamboov1.ValidatePolicyResponse{
			Valid:       false,
			Diagnostics: convertDiagnostics(err),
		}, nil
	}
	return &bamboov1.ValidatePolicyResponse{Valid: true}, nil
}

// EvaluateAccess answers "can src reach dst:port?" against the current
// persisted policy. Useful for debugging / dry-run flows.
func (h *PolicyHandler) EvaluateAccess(ctx context.Context, req *bamboov1.EvaluateAccessRequest) (*bamboov1.EvaluateAccessResponse, error) {
	tenant, err := h.resolveTenant(ctx)
	if err != nil {
		return nil, err
	}

	parsedPol, perr := h.loadParsedPolicy(ctx, tenant.ID)
	if perr != nil {
		return nil, perr
	}

	evalReq, err := buildEvalRequest(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	action, rule := parsedPol.Evaluate(evalReq)
	resp := &bamboov1.EvaluateAccessResponse{
		Action: protoAction(action),
	}
	matchedRuleID := ""
	if rule != nil {
		matchedRuleID = rule.ID
		resp.MatchedRule = &bamboov1.ACLRule{
			Id:           rule.ID,
			Action:       protoAction(rule.Action),
			Sources:      stringifySources(rule.Sources),
			Destinations: stringifyDestinations(rule.Destinations),
			Description:  rule.Description,
		}
		resp.Trace = []string{
			fmt.Sprintf("matched rule %q (%s)", rule.ID, action),
		}
	} else {
		resp.Trace = []string{"no rule matched; default deny"}
	}

	if err := h.traces.Insert(ctx, &clickhouse.EvaluationTrace{
		TenantID:      tenant.ID,
		Source:        req.GetSource(),
		Destination:   req.GetDestination(),
		Port:          uint16(req.GetPort()),
		Protocol:      req.GetProtocol(),
		Action:        action.String(),
		MatchedRuleID: matchedRuleID,
	}); err != nil {
		slog.Warn("clickhouse evaluation trace insert failed", "err", err)
	}

	return resp, nil
}

// ListRecommendations returns Tier-1 (rule-based) suggestions for the
// calling tenant. Phase 1 covers three kinds:
//   - KIND_REMOVE_UNUSED_RULE      rules with zero hits in the window
//   - KIND_TIGHTEN_OVERPRIVILEGED  rules whose wildcard ports hit only
//     a small distinct set of ports
//   - KIND_BROADEN_NEEDED          (source, dest, port) triples being
//     default-denied above the noise floor
func (h *PolicyHandler) ListRecommendations(ctx context.Context, _ *bamboov1.ListRecommendationsRequest) (*bamboov1.ListRecommendationsResponse, error) {
	tenant, err := h.resolveTenant(ctx)
	if err != nil {
		return nil, err
	}

	parsedPol, perr := h.loadParsedPolicy(ctx, tenant.ID)
	if perr != nil {
		return nil, perr
	}

	since := time.Now().Add(-30 * 24 * time.Hour)

	hits, err := h.traces.RuleHitCounts(ctx, tenant.ID, since)
	if err != nil {
		slog.Warn("clickhouse rule hit query failed; running with empty hits", "err", err)
		hits = map[string]uint64{}
	}

	chObs, err := h.traces.RuleObservations(ctx, tenant.ID, since)
	if err != nil {
		slog.Warn("clickhouse rule observation query failed", "err", err)
		chObs = nil
	}
	obs := translateObservations(chObs)

	chFlows, err := h.traces.TopDeniedFlows(ctx, tenant.ID, since, 10, 5)
	if err != nil {
		slog.Warn("clickhouse denied-flow query failed", "err", err)
		chFlows = nil
	}
	flows := translateDeniedFlows(chFlows)

	// Tier-2 anomalies: surface ML-flagged events from the last 24h
	// at score >= 0.6 (conservative threshold; tune per-tenant later).
	chFindings, err := h.anomalies.RecentByTenant(ctx, tenant.ID, time.Now().Add(-24*time.Hour), 20)
	if err != nil {
		slog.Warn("clickhouse anomaly query failed", "err", err)
		chFindings = nil
	}
	findings := translateFindings(chFindings)

	combined := recommend.UnusedRules(parsedPol, hits, since)
	combined = append(combined, recommend.OverPrivilegedRules(parsedPol, obs, since)...)
	combined = append(combined, recommend.BroadenNeeded(parsedPol, flows, since)...)
	combined = append(combined, recommend.Anomalies(findings, 0.6)...)

	resp := &bamboov1.ListRecommendationsResponse{
		Recommendations: make([]*bamboov1.Recommendation, 0, len(combined)),
	}
	for _, r := range combined {
		resp.Recommendations = append(resp.Recommendations, &bamboov1.Recommendation{
			Id:          r.ID.String(),
			Kind:        recommendKindToProto(r.Kind),
			Summary:     r.Summary,
			Diff:        r.Diff,
			Evidence:    r.Evidence,
			Confidence:  r.Confidence,
			GeneratedAt: timestamppb.New(r.GeneratedAt),
		})
	}
	return resp, nil
}

// translateFindings maps clickhouse.AnomalyFinding into the
// recommend-package equivalent.
func translateFindings(in []clickhouse.AnomalyFinding) []recommend.AnomalyFinding {
	out := make([]recommend.AnomalyFinding, len(in))
	for i, f := range in {
		out[i] = recommend.AnomalyFinding{
			ID:           f.ID,
			OccurredAt:   f.OccurredAt,
			GeneratedAt:  f.GeneratedAt,
			Score:        f.Score,
			ModelVersion: f.ModelVersion,
			EventSummary: f.EventSummary,
		}
	}
	return out
}

// translateDeniedFlows maps the ClickHouse-side struct into the
// recommend-package equivalent.
func translateDeniedFlows(in []clickhouse.DeniedFlow) []recommend.DeniedFlow {
	out := make([]recommend.DeniedFlow, len(in))
	for i, f := range in {
		out[i] = recommend.DeniedFlow{
			Source:      f.Source,
			Destination: f.Destination,
			Port:        f.Port,
			Hits:        f.Hits,
		}
	}
	return out
}

// translateObservations maps the ClickHouse-side aggregation into the
// recommend-package shape so the recommender stays driver-agnostic.
func translateObservations(chObs map[string]*clickhouse.RuleObservation) map[string]*recommend.RuleObservation {
	if chObs == nil {
		return nil
	}
	out := make(map[string]*recommend.RuleObservation, len(chObs))
	for id, o := range chObs {
		out[id] = &recommend.RuleObservation{
			RuleID:    o.RuleID,
			Ports:     o.Ports,
			TotalHits: o.TotalHits,
		}
	}
	return out
}

func recommendKindToProto(k recommend.Kind) bamboov1.Recommendation_Kind {
	switch k {
	case recommend.KindRemoveUnusedRule:
		return bamboov1.Recommendation_KIND_REMOVE_UNUSED_RULE
	case recommend.KindTightenOverPrivileged:
		return bamboov1.Recommendation_KIND_TIGHTEN_OVERPRIVILEGED
	case recommend.KindBroadenNeeded:
		return bamboov1.Recommendation_KIND_BROADEN_NEEDED
	case recommend.KindFlagAnomalous:
		return bamboov1.Recommendation_KIND_FLAG_ANOMALOUS
	default:
		return bamboov1.Recommendation_KIND_UNSPECIFIED
	}
}

// loadParsedPolicy fetches and parses the current policy. Returns an
// empty Policy (default-deny everything) when no record exists.
func (h *PolicyHandler) loadParsedPolicy(ctx context.Context, tenantID uuid.UUID) (*policy.Policy, error) {
	rec, err := h.policies.Get(ctx, tenantID)
	if errors.Is(err, repo.ErrNotFound) {
		return &policy.Policy{}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load policy: %v", err)
	}
	parsed, err := policy.Parse("policy.hcl", rec.HCLSource)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "parse persisted policy: %v", err)
	}
	return parsed, nil
}

// resolveTenant centralises the metadata-fallback path used by every
// handler in this file.
func (h *PolicyHandler) resolveTenant(ctx context.Context) (*repo.Tenant, error) {
	slug := tenantSlugFromMetadata(ctx)
	t, err := h.tenants.GetOrCreate(ctx, slug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tenant resolve: %v", err)
	}
	return t, nil
}

// buildEvalRequest converts a proto EvaluateAccessRequest into the
// internal EvalRequest the policy package consumes.
func buildEvalRequest(req *bamboov1.EvaluateAccessRequest) (policy.EvalRequest, error) {
	out := policy.EvalRequest{
		DstPort: uint16(req.GetPort()),
	}

	src := req.GetSource()
	if src == "" {
		return out, fmt.Errorf("source is required")
	}
	prefix, name, ok := splitMatcher(src)
	if !ok {
		return out, fmt.Errorf("invalid source %q", src)
	}
	switch prefix {
	case "user":
		out.SrcUser = name
	case "group":
		out.SrcGroups = []string{name}
	case "tag":
		out.SrcTags = []string{name}
	case "peer", "ip":
		addr, err := netip.ParseAddr(name)
		if err != nil {
			return out, fmt.Errorf("invalid source ip: %w", err)
		}
		out.SrcIP = addr
	default:
		return out, fmt.Errorf("unknown source prefix %q", prefix)
	}

	dst := req.GetDestination()
	if dst == "" {
		return out, fmt.Errorf("destination is required")
	}
	dprefix, dname, ok := splitMatcher(dst)
	if !ok {
		// Treat as IP literal.
		addr, err := netip.ParseAddr(dst)
		if err != nil {
			return out, fmt.Errorf("invalid destination %q", dst)
		}
		out.DstIP = addr
		return out, nil
	}
	switch dprefix {
	case "tag":
		out.DstTags = []string{dname}
	case "ip":
		addr, err := netip.ParseAddr(dname)
		if err != nil {
			return out, fmt.Errorf("invalid destination ip: %w", err)
		}
		out.DstIP = addr
	default:
		return out, fmt.Errorf("unknown destination prefix %q", dprefix)
	}
	return out, nil
}

// splitMatcher extracts "prefix:value" from a matcher string.
func splitMatcher(s string) (string, string, bool) {
	for i, r := range s {
		if r == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// convertDiagnostics maps an hcl.Diagnostics error into the proto form.
func convertDiagnostics(err error) []*bamboov1.PolicyDiagnostic {
	var diags hcl.Diagnostics
	if errors.As(err, &diags) {
		out := make([]*bamboov1.PolicyDiagnostic, 0, len(diags))
		for _, d := range diags {
			pd := &bamboov1.PolicyDiagnostic{
				Severity: severityFromHCL(d.Severity),
				Message:  d.Summary + ": " + d.Detail,
			}
			if d.Subject != nil {
				pd.Line = int32(d.Subject.Start.Line)     //nolint:gosec // line numbers fit in int32
				pd.Column = int32(d.Subject.Start.Column) //nolint:gosec // column numbers fit in int32
			}
			out = append(out, pd)
		}
		return out
	}
	return []*bamboov1.PolicyDiagnostic{{
		Severity: bamboov1.PolicyDiagnostic_SEVERITY_ERROR,
		Message:  err.Error(),
	}}
}

func severityFromHCL(s hcl.DiagnosticSeverity) bamboov1.PolicyDiagnostic_Severity {
	switch s {
	case hcl.DiagWarning:
		return bamboov1.PolicyDiagnostic_SEVERITY_WARNING
	default:
		return bamboov1.PolicyDiagnostic_SEVERITY_ERROR
	}
}

func protoAction(a policy.Action) bamboov1.Action {
	switch a {
	case policy.ActionAllow:
		return bamboov1.Action_ACTION_ALLOW
	case policy.ActionDeny:
		return bamboov1.Action_ACTION_DENY
	default:
		return bamboov1.Action_ACTION_UNSPECIFIED
	}
}

// toProtoPolicy converts the persisted record + parsed view into the
// proto type. groups and tags are left empty: those come from the
// user_groups / tags tables (not yet wired) rather than the DSL.
func toProtoPolicy(rec *repo.PolicyRecord, parsed *policy.Policy) *bamboov1.Policy {
	out := &bamboov1.Policy{
		TenantId:  rec.TenantID.String(),
		Revision:  rec.Revision,
		HclSource: rec.HCLSource,
		UpdatedAt: timestamppb.New(rec.UpdatedAt),
	}
	if rec.UpdatedBy != nil {
		out.UpdatedBy = rec.UpdatedBy.String()
	}
	if parsed != nil {
		out.Rules = make([]*bamboov1.ACLRule, 0, len(parsed.Rules))
		for _, r := range parsed.Rules {
			out.Rules = append(out.Rules, &bamboov1.ACLRule{
				Id:           r.ID,
				Action:       protoAction(r.Action),
				Sources:      stringifySources(r.Sources),
				Destinations: stringifyDestinations(r.Destinations),
				Description:  r.Description,
			})
		}
	}
	return out
}

// stringifySources renders matchers in their DSL string form for the
// proto's repeated string fields.
func stringifySources(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatSource(m))
	}
	return out
}

func stringifyDestinations(ms []policy.Matcher) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, formatSource(m)+":"+m.Ports.String())
	}
	return out
}

func formatSource(m policy.Matcher) string {
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
