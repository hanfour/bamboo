# 0012. Phase 1 → Phase 2 Transition

- **Status**: Accepted
- **Date**: 2026-05-08
- **Deciders**: founders
- **Companion**: [Phase 1 retrospective](../development/phase-1-retro.md)

## Context

Twelve squash-merged PRs took bamboo from an empty repo to a working
mesh-VPN control plane with persistent storage, telemetry, a
SwiftUI-shaped macOS client, a Next.js web UI, three Tier-1 AI
recommendation kinds, and CI-locked governance. The retrospective
captures what landed in detail.

That was Phase 1. We now have to draw a line: what closes Phase 1, what
opens Phase 2, and what changes about how we ship.

## Decision

### Phase 1 closes when

All of the following are true (currently the **bold** items are the
only blockers; everything else is already met):

- ✅ gRPC services for Auth, Coordinator, Policy, Telemetry are wired
  end-to-end with at least one concrete handler each.
- ✅ Postgres schema v1 + ClickHouse telemetry tables are migrated
  + tested in CI.
- ✅ AI Tier 1 (rule-based recommendations) is delivered: unused-rule,
  over-privileged ports, broaden-needed.
- ✅ A working Linux client (`bamboo` CLI) brings up a real WireGuard
  interface against a real controller.
- ✅ Web UI renders live data from the REST bridge (Dashboard, Peers,
  ACL with AI suggestions).
- ✅ CI ruleset enforces six required checks on every PR.
- ⏳ **One end-to-end demo**: minted PreAuthKey, two Linux peers
  register and ping each other across the tunnel, ACL update is
  reflected, an AI recommendation surfaces in the Web UI.
- ⏳ **Phase 1 announcement**: a written demo recording or
  walkthrough document operators can hand to evaluators.

We declare Phase 1 closed when the two outstanding items land. They
are tracked as separate issues; this ADR does not block them.

### Phase 2 begins with the following themes

The themes are deliberately small in number. Each maps to specific
work; none is open-ended.

1. **AI Tier 2 — ML-driven anomaly detection.** Per-tenant Isolation
   Forest models trained nightly over `connection_events`, with
   anomaly scores surfaced as a fourth recommendation kind alongside
   the three Tier-1 kinds. Implementation lands in the new `apps/ai`
   Python module (separate from the Go control plane to keep the ML
   toolchain isolated). See ADR 0010 §Tier 2.

2. **AI Tier 3 — Natural-language ACL DSL.** A small Claude-Sonnet
   integration that turns "let staging reach the metrics service on
   9090" into a proposed `rule` block. Lands in `apps/ai` once Tier 2
   is shipping. ADR 0010 §Tier 3.

3. **Real OIDC + per-user UI.** Live Google / GitHub credentials,
   web UI gains a session cookie, the bearer JWT carries `user_id`
   and `tenant_id` from claims rather than from the X-Tenant-Slug
   header. The `default` fallback stops being the production path.

4. **macOS app real build + signed artifact.** Apple developer team
   enrollment, NetworkExtension entitlement, Xcode project generated
   from `project.yml`. First production build target.

5. **Production hardening.** Third-party pen test, SOC 2 readiness
   gap analysis, audit-log immutability (append-only verification),
   Helm chart real content, multi-region (Tokyo + Seoul) baseline.

6. **Customer onboarding playbook.** Documentation + scripts that
   take a fresh tenant from "I have credentials" to "two peers
   talking" in ten minutes.

### What does not change

- License model stays AGPLv3 (server) + Apache 2.0 (clients) per
  ADR 0001. The transition window for `clients/core` (ADR 0011) is
  still open and will close when we have invested in the clean-room
  rewrite. Phase 2 does not force that close; it does require
  re-evaluating the timeline.
- Cloud strategy stays AWS Tokyo + Cloudflare edge + Vultr relays
  per ADR 0009. Phase 2 may add Seoul as DR but does not redraw the
  boundary.
- LLM-provider strategy stays multi-provider per ADR 0010.
- Branch protection ruleset + auto-merge stay the default workflow.

### What changes

- **Cadence shifts from features to integrations.** Phase 1 was
  scaffold and feature-velocity. Phase 2 is "the thing actually
  works for a tenant we did not build it for". PRs should
  preferentially close issues with named owners and shipped demos
  rather than expand the surface area.
- **ADRs become the gating mechanism.** Schema changes, new external
  dependencies (LLM providers, S3 backends, NAT-traversal protocols)
  require an ADR before code lands. Phase 1 was permissive about
  this; Phase 2 is not.
- **Per-feature flags become the default.** Phase 2 features ship
  behind a per-tenant configuration toggle so we can roll-out without
  a global cutover. The implementation detail is small (a row on
  tenants); the discipline is big.

## Consequences

### Positive

- We stop accumulating surface area without integrating it. A clear
  Phase 2 framing means each PR has a stronger "why now".
- The two outstanding Phase 1 close-out items create a forcing
  function for an end-to-end demo.
- Customer onboarding and pen-test readiness are explicit tracks
  rather than emergent slips.

### Negative / Trade-offs

- Themes 1–6 are wide. We will inevitably defer at least one to
  Phase 3 if velocity stays the same.
- ADR-as-gate slows down small-feature work. We accept that cost
  because the alternative (drift between docs and code) is worse
  for a project that intends to be open source and audited.
- macOS app real build depends on Apple developer-program timing
  outside our control.

## Alternatives Considered

- **No formal Phase 2 line.** Just keep landing things. Rejected
  because the AI tiers + production hardening + customer onboarding
  pull in different directions and need explicit prioritization.
- **Push Phase 2 ML into the existing Go control plane.** Rejected
  because Python's ML ecosystem is overwhelmingly stronger and
  isolating the toolchain matches the ADR 0010 architecture.
- **Hire-and-grow before Phase 2.** Out of scope for an
  individual-founder repo today; revisit when Phase 2 themes are
  half-shipped.

## References

- [Phase 1 retro](../development/phase-1-retro.md)
- [ADR 0001 — License Strategy](./0001-license-strategy.md)
- [ADR 0009 — Cloud Provider Strategy](./0009-cloud-provider-strategy.md)
- [ADR 0010 — LLM Multi-Provider Strategy](./0010-llm-multi-provider-strategy.md)
- [ADR 0011 — Client Core Re-licensing Path](./0011-client-core-relicensing-path.md)
