# Project goals (2026-09-23)

Audit of bamboo against the stated product
(`README.md`: AI-native zero-trust mesh), ADR 0012 Phase 2 themes,
the 2026-05-17 feature-gap roadmap, and the code on
`rls-tenant-backstop` (5 commits ahead of `origin/main` at `3407b8d`).

This file is the assignment list. Historical roadmaps stay as records;
several of their open items have since shipped and are listed under
"Already done" so they are not re-opened.

## Product goal

A tenant can sign in, enroll peers, and have controller policy show up
as WireGuard `AllowedIPs`. A forgotten `tenant_id` check must fail
closed in Postgres, not leak another tenant's peers. AI recommendations
stay advisory. Windows GUI, billing, and a natural-language ACL DSL are
not part of this cycle.

## P0 — Finish the tenant-isolation backstop (ADR 0014)

Status: in progress on `rls-tenant-backstop`.

Done:

- Step A — `db.Querier` + `db.WithTenant` (`app.tenant_id` tx-local GUC).
- Step B — repositories take `Querier`.
- Step C slice 1 — gRPC `PolicyHandler` reads/writes inside `WithTenant`.
- Step C slice 2 — webhook subscription handlers.
- Step C slice 3 — API-token admin handlers. Their `insertAudit` calls
  still run on the pool, outside the tenant transaction.

Not done, in order:

1. **Step C remainder.** Every query against a tenant-scoped table
   (`users`, `peers`, `tags`, `peer_tags`, `acl_policies`,
   `acl_policy_history`, `pre_auth_keys`, `audit_log`,
   `user_invitations`, `webhook_subscriptions`, `api_tokens`) that
   already knows the caller's tenant must run inside `WithTenant`.
   Audit inserts go in that same transaction, inside a savepoint, so a
   failed audit does not abort the user-facing write and does not
   fail the request.
   Do not hold a transaction across a Watch / SSE stream.
   Do not wrap these yet:
   - `tenants` lookups and `relay_servers` (global, ADR 0013).
   - Cross-tenant jobs: invite reaper, audit retention reaper,
     revoked-session reaper, relay-health reaper, NAT64 egress-health
     reaper, `ListNAT64EgressActiveTenants`, `MarkOfflineExcept`,
     metrics aggregate scans.
   - Bootstrap reads that discover `tenant_id`: API-token `GetByID`,
     pre-auth-key redeem by secret, invitation-token resolve, OIDC
     user upsert before the tenant is known. Mark them
     `RLS bootstrap` and leave them on the pool.
2. **Bootstrap lookup.** Those four reads return zero rows once RLS is
   forced. Add a `SECURITY DEFINER` lookup (or equivalent) that returns
   only the row for a presented secret/id, then the handler sets
   `WithTenant` for everything after. Design it before enabling RLS.
3. **Step D.** `BYPASSRLS` role, or one audited maintenance sentinel,
   used only by the cross-tenant jobs above.
4. **Step E.** Migration that `ENABLE` + `FORCE` ROW LEVEL SECURITY and
   creates `tenant_isolation` policies. Not before 1–3.
5. **Step F.** Un-skip `apps/controller/test/e2e/rls_backstop_test.go`.
   It must pass. Add the same contract for one write (`WITH CHECK`).

Done when: a bare `SELECT` inside `app.tenant_id=<A>` sees only A's
peers; a handler that forgets `WithTenant` sees nothing; reapers still
run; redeeming a pre-auth key and calling an API token still work.

## P1 — Defects that are still real

- **`user:` / `group:` ACL matchers do not reach the wire.**
  `docs/demo.md` states they evaluate in preview and `EvaluateAccess`
  but do not contribute to `AllowedIPs`. Close that gap or stop
  offering those matchers in the editor.
- **API-token and webhook audit rows sit outside `WithTenant`**
  (`api.go` `insertAudit` after the callback). They will vanish under
  RLS. Fold them into step C.
- **ADR index is stale.** `docs/adr/README.md` stops at 0012.
  0013 (relay protocol, Accepted) and 0014 (RLS, Proposed) exist.
- **README layout is stale.** `clients/macos` is `clients/apple`.
  `clients/linux` and `clients/windows` READMEs still say scaffolding;
  the Linux agent is `clients/cli`. `clients/core/README.md` still says
  handlers are stubs and `wgctrl` is unwired.
- **Phase 1 close-out in ADR 0012 is stale.** The two blockers (e2e demo
  script, walkthrough doc) exist (`scripts/demo.sh`,
  `docs/development/phase-1-demo-walkthrough.md`). The ADR still marks
  them open. Update the ADR; do not rebuild the demo.

## P2 — Specified, not started (do not pull into P0)

- **AI Tier 2 delivery.** `apps/ai` trains and scores Isolation Forest
  on synthetic data. Nothing schedules training, stores the model, or
  turns a score into the existing `FLAG_ANOMALOUS` recommendation.
  Needs the deployment ADR the module README already calls for.
- **Apple release signing.** Connect / tunnel / relay / DNS64 are
  implemented. `clients/apple/README.md` still has code-signed
  installer and OIDC `ASWebAuthenticationSession` paused.
- **Customer onboarding playbook** (ADR 0012 theme 6) beyond the
  existing single-VPS and demo docs.
- **`clients/core` clean-room** (ADR 0011). NetBird `encryption` and
  `pkg/base62` are imported; the rewrite has not started.

## Explicitly out of this cycle

- Tenant billing / plan tier.
- ACL editor syntax highlighting (CodeMirror).
- Windows native GUI (`clients/windows` has no code).
- Go / TypeScript / Python SDK implementations (READMEs only).
- AI Tier 3 natural-language ACL DSL.
- Third-party penetration test.
- IPv6-only client onboarding and WireGuard-underlay-over-IPv6
  (NAT64 phases A–C, including Tayga egress, DNS64, and failover,
  already shipped through PR #249).

## Already done — do not re-file

P0/P1 of the 2026-05-17 roadmap (ACL enforcement, device approval, ACL
editor, auth required in prod, subnet routes, exit nodes, connection
log, tag owners), the 2026-05-25 P2 batch (webhooks, API tokens, audit
immutability and retention, multi-relay, bandwidth, session revocation,
GDPR erase, version-upgrade indicator), and NAT64 phases A–C.

## This cycle's assignment

Wave 1 landed in the working tree on 2026-09-23 (not committed).
HTTP heartbeat, watch, approve, reject, and status now stamp the
already-verified tenant onto the context so those coordinator reads
do not stay on the pool. RLS is not enabled. Bootstrap lookups
(API-token `GetByID`, pre-auth redeem, dev-mode heartbeat with no
credential) are still on the pool on purpose.

Wave 1 (parallel, no RLS migration):

| Slice | Files | Outcome |
|---|---|---|
| C4 | `handlers/auth.go`, `handlers/coordinator.go`, `peer_dns_name.go` if it hits tenant tables | Tenant-known gRPC queries run in `WithTenant` |
| C5 | `server/api_peers.go`, `server/api_relay_token.go` | Register / heartbeat / watch / relay-token peer reads |
| C6 | `server/api.go`, `server/api_admin_users.go`, `server/api_audit_export.go` | REST admin/policy/user paths, and existing token/webhook audits moved inside the tenant tx |

Wave 1 was reviewed against a throwaway Postgres: `go test ./internal/handlers/ ./internal/server/ ./internal/db/` and `go test ./test/e2e/` both passed (migrations 00001–00021 applied first). RLS is still off, so those tests do not prove the backstop.

Wave 2 landed in the working tree on 2026-09-23 (not committed). Migration `00022_rls_backstop.sql` creates `bamboo_app` (subject to RLS, `NOINHERIT`) and `bamboo_maintenance` (`BYPASSRLS`). The controller pool assumes `bamboo_app` in `AfterConnect`, so a superuser DSN still hits the policies. Bootstrap reads and cross-tenant jobs use `WithBypass` (`SET LOCAL ROLE bamboo_maintenance`). `rls_backstop_test.go` is un-skipped and also rejects a cross-tenant insert (`WITH CHECK`) and a query with no GUC.

Verified on a throwaway Postgres: migration through 00022, `go test ./internal/db/... ./internal/metrics/ ./test/e2e/` passed.
