# Phase 1 Retrospective

This document captures what shipped in Phase 1 (initial scaffolding through
the AI Tier-1 trio), what was deferred, and the lessons that will shape
Phase 2 planning. Companion to [ADR 0012 — Phase 2 Transition](../adr/0012-phase-2-transition.md).

Baseline: PR #20 (Sprint 2 ticket plan) through PR #32 (Tier-1 broaden
recommendation). Twelve squash-merged PRs over the period; every PR
landed via the GitHub ruleset (six required CI jobs + auto-merge).

## What shipped

### Mesh data plane

- Curve25519 key pair generation with RFC 7748 clamping
  (`clients/core/wg`).
- WireGuard interface bring-up on Linux via `wgctrl` + `vishvananda/netlink`
  (`clients/core/device`). Cross-platform `Device` interface; `device_other.go`
  returns `ErrUnsupported` so non-Linux callers fail gracefully.
- NaCl-box encrypt / decrypt + proto wrappers
  (`clients/core/encryption`, imported from netbirdio/netbird and adapted
  to `log/slog` + `google.golang.org/protobuf/proto`).
- `bin/bamboo` CLI with `up / down / status / version`. XDG-aware key
  persistence at `~/.config/bamboo/private_key`.
- `bin/dev-agent` minimal smoke binary.
- macOS client shell: `BambooApp` + `PacketTunnelProvider` Swift
  sources, XcodeGen `project.yml`, full prerequisite README.

### Control plane

- gRPC services: `Auth`, `Coordinator`, `Policy`, `Telemetry`. Reflection
  enabled. Protos in `proto/bamboo/v1/*.proto` with `buf` lint + drift CI.
- HTTP server alongside gRPC: `/auth/{provider}/login`, `/auth/{provider}/callback`,
  `/healthz`, `/api/v1/{overview,peers,policy,recommendations}`.
- Pre-auth keys: Argon2id-hashed, prefix-routed for indexed lookup,
  one-time + reusable variants, redeem-bound to tenant.
- OIDC scaffold: Google + GitHub providers using `golang.org/x/oauth2`,
  state-token CSRF, HMAC-SHA256 session JWTs. HTML callback path
  functional; gRPC `CompleteOIDCFlow` deliberately Unimplemented.
- Coordinator: real Register, Heartbeat, server-streaming WatchPeers
  with an in-memory event bus (`internal/events`).
- Policy: full ACL DSL parser (HCL v2 + gohcl), evaluator with
  first-match-wins + default-deny, write-through with optimistic
  concurrency on `revision`.
- Audit log: every state-changing handler writes to `audit_log`
  (peer.register, preauthkey.{create,redeem,revoke}, policy.update).

### AI Tier 1

- **Unused rule** detector — flags rules with zero hits in the
  observation window. Confidence scales with window length.
- **Over-privileged ports** detector — for rules whose destinations
  use wildcard ports but observed traffic landed on a small distinct
  set, propose tightening. Heuristic thresholds: 50 hit minimum, 10
  distinct ports maximum.
- **Broaden-needed** detector — surfaces high-frequency
  default-deny triples as candidates for new allow rules.
  Confidence capped at 0.85; broadening is inherently riskier than
  removing or tightening.

All three kinds emit a unified-diff fragment + evidence bullets +
confidence score. None auto-apply; the operator opens a PR.

### Persistence

- Postgres (11 tables): `tenants`, `users`, `user_groups`,
  `user_group_members`, `peers`, `tags`, `peer_tags`, `acl_policies`,
  `acl_policy_history`, `pre_auth_keys`, `audit_log`. Multi-tenant via
  `tenant_id` FK + index on every tenant-scoped table.
- Embedded migrations via `goose` + `embed.FS`. Controller binary is
  the source of truth.
- ClickHouse: `evaluation_traces` and `connection_events` with 90-day
  TTL. `Conn` is nil-safe so an unconfigured ClickHouse degrades to a
  single warning + drops.
- ClickHouse aggregations behind the recommender:
  `RuleHitCounts`, `RuleObservations`, `TopDeniedFlows`.

### Web UI

- Next.js 14 App Router, TypeScript strict, Tailwind 3, `next-intl`
  with English + Traditional Chinese (full coverage).
- Three pages backed by the REST bridge: Dashboard (overview tiles),
  Peers (live table), ACL (HCL source + structured rules + AI
  recommendations panel).
- Server-side fetch with friendly empty-state fallbacks; a single
  controller 5xx never blanks the UI.

### Governance

- `LICENSE-APACHE` (clients, SDKs, proto) + `LICENSE-AGPL` (server
  components). `LICENSING.md` is the per-directory map.
- Branch ruleset on `main`: PR required (0 approvals self-merge),
  required status checks, no force-push, no deletions, linear
  history. Auto-merge enabled.
- Six CI jobs run on every push: License headers, Proto lint + drift,
  golangci-lint, Unit + integration tests (with Postgres + ClickHouse
  service containers), Web (lint + build), Build binaries.
- Five Go modules in a `go.work` workspace + one Next.js project +
  one Swift project skeleton.
- 81 Go files carrying SPDX-License-Identifier headers; CI fails the
  PR if a hand-written file lacks one.

### NetBird import path

- `scripts/netbird-prep.sh` + `scripts/netbird-audit.sh` codify the
  read-only sandbox + brand audit + inventory drift check.
- Two modules imported and validated end-to-end: `pkg/base62` (small
  pure-Go encoding helper) and `clients/core/encryption` (NaCl-box
  with logrus → slog and proto migration).
- `docs/development/netbird-import-inventory.md` is the operating
  inventory; the audit script exits non-zero on drift.

## What was deferred

These were either explicit "land in Phase 2" choices or follow-ups
that grew out of Phase 1 scope:

- **Real OIDC HTML flow** — the route works, but no live Google /
  GitHub credentials have been exercised end-to-end. JWT bearer
  validation in `Coordinator.Register` is wired but tenant-bound only;
  user identity in claims lands when callback is exercised.
- **macOS app real build** — the source compiles on a properly
  configured Mac; the Apple developer team ID + NetworkExtension
  entitlement are pending external action.
- **Web UI auth** — uses a single tenant (`BAMBOO_TENANT` env var
  pinned to `default` for SSR). No session cookie, no per-user view.
- **`bamboo` daemon mode** — `up` is foreground; `WatchPeers` stream
  integration that keeps the peer set live across long sessions is
  Phase 2.
- **More NetBird imports** — `iface/` and `route/` in particular are
  inventoried but not yet imported. The clean-room rewrite
  obligation per ADR 0011 is unstarted.
- **Tier 2 + Tier 3 AI** — Isolation Forest anomaly detection +
  natural-language ACL DSL. ADR 0010 Tier-1 is done; the upper tiers
  are Phase 2.
- **Production-ready security audit** — no third-party penetration
  test yet. SOC 2 / ISO 27001 not in scope.
- **Persistent-volume backups, DR, multi-region failover** — single
  Tokyo region today. ADR 0009 contemplates Seoul as DR.
- **Helm chart real content** — directory exists; production chart
  contents land in Phase 2.

## What we learned

- **Auto-merge with a strict ruleset** kept the team from accidentally
  pushing to `main` and forced the discipline of a green CI for
  everything. The ruleset is invisible during the day and catches
  exactly when it matters (the `wget` healthcheck miss on the CH
  service container).
- **ClickHouse degraded mode** (the `IsConfigured()` predicate)
  saved us from making telemetry a hard dependency. Operators who do
  not need analytics can run the controller with no `clickhouse.url`.
  The same pattern should apply to other optional subsystems.
- **Two NetBird imports validate the SOP** but the AGPL-Apache
  partition discipline (ADR 0011) is the long-term cost we have not
  paid yet. Either we accept staying AGPL on `clients/core` for
  longer, or we plan dedicated rewrite sprints.
- **Heuristic Tier-1** carried the AI story further than expected.
  Per-rule hit aggregation alone gave us three recommender kinds.
  Tier 2 ML should be additive, not a replacement, and should keep
  the same operator-reviewed-PR pattern.
- **The HCL DSL is now load-bearing**. Adding a new matcher or rule
  property touches the parser, evaluator, recommenders, and the JSON
  bridge. Schema changes need an ADR going forward.
- **Web UI talks REST, not gRPC-Web**. That kept the bridge tiny and
  SSR-friendly, but it means the gRPC API is the canonical surface
  twice over (once for clients, once internally). No regrets, but
  consider gRPC-Gateway for write-side endpoints if the Web UI
  starts mutating state.

## Counts

| Metric | Value |
| --- | --- |
| PRs squash-merged | 12 |
| Commits on `main` | 19 |
| Go modules | 5 (`apps/controller`, `clients/core`, `clients/cli`, `proto`, `pkg/base62`) |
| TS / Swift / Python projects | 1 + 1 + (Phase 2) |
| Go files with SPDX headers | 81 |
| ADRs | 4 (0001, 0009, 0010, 0011) |
| GitHub Issues opened | 20 + organic |
| Required CI checks | 6 |

## Status of the original Sprint 2 issue board

| # | Title | Status |
| --- | --- | --- |
| #1 | Bootstrap monorepo Go workspaces | done |
| #2 | NetBird fork: sandbox clone + inventory | done (audit + 2 imports) |
| #3 | Import management → controller | TBD (live wiring instead) |
| #4 | Import relay → apps/relay | deferred |
| #5 | Database schema v1 multi-tenant | done |
| #6 | gRPC proto first cut | done (4 services) |
| #7 | CI pipeline first cut | done (6 jobs) |
| #8 | Staging environment | deferred (local-only) |
| #9 | Local dev stack | done (`make dev`) |
| #10 | NAT traversal harness | deferred |
| #11 | OIDC scaffold (Google + GitHub) | scaffold + JWT, real flow Phase 2 |
| #12 | PreAuthKey lifecycle | done |
| #13 | Peer registration handshake | done |
| #14 | Long-poll peer set updates | done |
| #15 | First end-to-end tunnel | done (Linux) |
| #16 | macOS client app shell | done; real build Phase 2 |
| #17 | Linux daemon + CLI | done (`bamboo`) |
| #18 | Web UI scaffold | done + live data |
| #19 | Audit log | done |
| #20 | Sprint 2 retrospective | this document |
