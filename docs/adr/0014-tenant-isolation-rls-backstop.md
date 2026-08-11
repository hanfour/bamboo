# 0014. Tenant isolation — Postgres RLS as a DB-level backstop

- **Status**: Proposed
- **Date**: 2026-08-11
- **Deciders**: founders
- **Related**: `docs/architecture/README.md` (§Multi-tenancy, previously
  "See ADR (TBD)"); the cross-tenant IDOR fixes on branch
  `fix-cross-tenant-authz` (peer-id REST + gRPC endpoints).

## Context

Multi-tenancy is enforced **entirely in application code**: every query
against a tenant-scoped table is expected to carry `AND tenant_id = $1`
(or to compare `row.TenantID` after an id-only fetch). There is **no
Postgres Row-Level Security** — `apps/controller/migrations/README` states
this explicitly, and `grep 'ROW LEVEL SECURITY'` over all 21 migrations
returns nothing.

That model has no backstop. A single forgotten check is a cross-tenant
breach, not a failed query. This is not hypothetical: a pre-iteration
review (2026-08-11) found and **confirmed by running e2e tests** two live
cross-tenant IDORs under `require_auth=true`:

- `GET /api/v1/peers/watch?peerId=<foreign>` streamed another tenant's
  netmap (hostnames, mesh IPs, WG pubkeys, NAT endpoints).
- `POST /api/v1/peers/heartbeat` rewrote a foreign peer's WireGuard
  endpoint (traffic-poisoning), proven by the victim row changing.

Both were single-line authorization omissions (`peerCredentialAllows` /
`enforcePeerBinding` accepted any valid JWT without binding it to the
target peer's tenant). They are now fixed, but the class remains: the
next id-only mutator that forgets its tenant check is one code review away
from the same outcome. The IDORs' low impact **today** rests only on the
dogfood running as a single `default` tenant; the moment bamboo serves
real tenants (a prerequisite for the deferred billing / plan-tier work),
the app-layer boundary is the only thing between tenants.

Constraint that shapes the decision: the controller shares **one
`pgxpool`** (`internal/db/db.go`, `MaxConns=20`) across all requests and
tenants, and repos call the pool directly with no per-request
transaction. RLS keyed on a *session* GUC would leak across pooled
requests; it must key on a *transaction-local* GUC.

## Decision

Adopt Postgres RLS as **defense-in-depth** (not a replacement for the
app-layer checks) on every tenant-scoped table, keyed on a
transaction-local GUC `app.tenant_id`.

### Policy shape

For each tenant-scoped table (`users`, `peers`, `tags`, `peer_tags`,
`acl_policies`, `acl_policy_history`, `pre_auth_keys`, `audit_log`,
`user_invitations`, `webhook_subscriptions`, `api_tokens`, and the
tenant-scoped ClickHouse-mirror source rows):

```sql
ALTER TABLE peers ENABLE ROW LEVEL SECURITY;
ALTER TABLE peers FORCE ROW LEVEL SECURITY;   -- applies to the table owner too
CREATE POLICY tenant_isolation ON peers
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
```

`current_setting('app.tenant_id', true)` returns NULL when the GUC is
unset; `tenant_id = NULL` matches no rows, so an un-scoped query **fails
closed** (returns nothing / rejects the write) rather than leaking. That
is the desired safety property — but it means every tenant-scoped query
path MUST set the GUC first.

`relay_servers` is deliberately **excluded** (it has no `tenant_id`; it is
a global, admin-managed registry by design — see ADR 0013).

### The GUC-setting seam (the load-bearing part)

Because there is no per-request transaction today, we introduce one for
tenant-scoped work:

1. A `Querier` interface (`Exec/Query/QueryRow`) satisfied by both
   `*pgxpool.Pool` and `pgx.Tx`. Repos take a `Querier` instead of
   `*db.Pool` so they can run inside a scoped transaction.
2. A helper `db.WithTenant(ctx, pool, tenantID, func(q Querier) error)`
   that `BEGIN`s a tx, runs `SELECT set_config('app.tenant_id', $1, true)`
   (tx-local), invokes the callback with the tx as `Querier`, and
   commits/rolls back.
3. Request handlers resolve the caller's tenant (from the verified JWT /
   API-token / peer-session claim — never a client header) and wrap their
   repo calls in `WithTenant`.

### Cross-tenant maintenance jobs

Several jobs legitimately span tenants and must bypass RLS:
`audit` retention reaper, invitation-expiry reaper, relay-health reaper,
`Peers.MarkOfflineExcept`, `ListNAT64EgressActiveTenants`, and the metrics
collector's aggregate scans. They run under a dedicated DB role with
`BYPASSRLS`, or set a sentinel `app.tenant_id` reserved for
maintenance. This must be explicit and audited — it is the one place the
backstop is intentionally off.

### Rollout ordering (critical — do NOT reorder)

Enabling RLS before the seam exists would make every query return zero
rows and take down the controller. Therefore:

1. **This ADR + a pending RLS contract test** (RED, `t.Skip`-gated so CI
   stays green): `apps/controller/test/e2e/rls_backstop_test.go`.
2. `Querier` abstraction + `WithTenant`; thread through every
   tenant-scoped repo path; keep behavior identical (pool still works).
3. `BYPASSRLS` role (or maintenance sentinel) for the cross-tenant jobs.
4. Migration that `ENABLE`/`FORCE`s RLS + creates the policies — behind a
   staged rollout, verified against a throwaway Postgres first.
5. Un-skip the contract test; it must go green.

## Consequences

### Positive

- The next forgotten `WHERE tenant_id` becomes a no-op (RLS filters the
  rows) instead of a cross-tenant breach. Fail-closed by construction.
- Makes the "real multi-tenant SaaS" posture (billing / plan-tier)
  defensible; the app-layer checks become the fast path, RLS the safety
  net.
- Satisfies the SOC 2 / tenant-isolation story the audit chain gestures at.

### Negative / Trade-offs

- Per-request transaction overhead on tenant-scoped paths (one extra
  round-trip for `set_config`, plus BEGIN/COMMIT). Acceptable; the
  heartbeat hot path already does multiple statements (and is being
  optimized separately).
- Repo signature churn: `*db.Pool` → `Querier` across the repo layer.
  Mechanical but wide.
- A path that forgets `WithTenant` returns **zero rows** — a loud,
  test-catchable failure, but a failure. This is the intended trade
  (loud emptiness over silent leakage).
- Cross-tenant jobs need a privileged bypass; that bypass is now a thing
  to guard and audit.

### Neutral

- `relay_servers` stays global; relay isolation continues to rely on the
  verified per-session token tenant (ADR 0013), not RLS.

## Alternatives Considered

- **App-layer only (status quo)** — rejected: proven fragile (two
  confirmed IDORs from single-line omissions; no backstop).
- **Session GUC set on pool `AfterConnect`** — rejected: pooled
  connections are reused across tenants/requests; a session-level
  `SET app.tenant_id` leaks to the next borrower. Must be tx-local.
- **Schema-per-tenant or database-per-tenant** — rejected: breaks the
  single-deployment / single-pool model, multiplies migration and
  connection overhead, and doesn't fit the "one deployment, many small
  tenants" product shape.
- **Trust the app layer + add a linter that requires `tenant_id` in every
  query** — rejected as the *primary* control (static analysis can't see
  id-only fetch-then-check patterns), but worth adding as a complementary
  guard later.

## References

- `docs/architecture/README.md` §Multi-tenancy (this ADR fills its TBD).
- ADR 0013 (relay protocol — why `relay_servers` is global).
- Branch `fix-cross-tenant-authz`: the REST + gRPC IDOR fixes and their
  e2e regression tests (`crosstenant_authz_test.go`,
  `crosstenant_grpc_authz_test.go`) that motivated this backstop.
- PostgreSQL RLS: https://www.postgresql.org/docs/current/ddl-rowsecurity.html
