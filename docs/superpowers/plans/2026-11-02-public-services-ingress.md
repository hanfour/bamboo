# Public Services Ingress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a service on a mesh peer reachable from the public internet at `https://<label>.svc.miilink.net`, configured and approved in the controller, served by Caddy (TLS) and a new `bamboo-ingress` reverse proxy on the operator's VPS that forwards over WireGuard.

**Architecture:** The controller gains an `ingress_services` table (RLS-scoped, with `SECURITY DEFINER` lookups for the cross-tenant label checks), admin REST, two internal endpoints (`ask` for Caddy on-demand TLS, `routes` for the proxy) plus a telemetry ingest endpoint, an Ed25519 ingress service token, and generated ACL rules for live services. A new Go module `apps/ingress` polls the routes table and reverse-proxies by `Host` with rate limits, a body cap, unbuffered streaming, and idle timeouts. `infra/full` adds a Caddy site block, a host-network compose service behind a compose profile, and deployment docs. The relay protocol and existing sites are not touched.

**Tech Stack:** Go 1.25 (controller, ingress; `net/http/httputil.ReverseProxy`, pgx v5, goose, ClickHouse driver, prometheus/client_golang v1.23.2), PostgreSQL 16 RLS, Caddy 2 on-demand TLS, Next.js 14 + next-intl + vitest (web), docker compose.

**Spec:** `docs/design/2026-11-02-public-services-ingress.md` (binding). Background research: `bamboo-ingress-research.md` (scratchpad; file:line references, read-only).

## Global Constraints

- Public name: `https://<label>.svc.miilink.net`; domain `*.svc.miilink.net`; one certificate per name via Caddy on-demand TLS gated by the controller; no wildcard certificate.
- TLS terminates on the operator's VPS (Caddy); Caddy forwards plain HTTP to `bamboo-ingress`, which forwards over WireGuard to `<peer mesh IP>:<target_port>`.
- The relay protocol is not touched. The VPS joins the tenant as an ordinary Linux peer tagged `tag:ingress`. One ingress peer serves one tenant.
- Label: `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`, globally unique across tenants. Reserved labels: `www`, `api`, `admin`, `relay`, `controller`, `ingress`, `mail`.
- `target_port`: 1024–65535. `enabled` default false. `approved_by` null = pending.
- A service is **live** when `enabled AND approved_by IS NOT NULL` and its peer is approved and not deleted.
- Cross-tenant label uniqueness check and `label → service` lookup go through a `SECURITY DEFINER` bootstrap function (ADR 0014, project-goals P0).
- REST: `GET /api/v1/ingress`, `POST /api/v1/peers/{id}/ingress` (`{label, target_port}`, pending), `POST /api/v1/ingress/{sid}/approve` (admin only), `PATCH /api/v1/ingress/{sid}` (`{enabled}`; changing `target_port` resets approval), `DELETE /api/v1/ingress/{sid}`.
- Every mutation writes `audit_log` through `writePeerAuditTx`; actions `ingress.create`, `ingress.approve`, `ingress.enable`, `ingress.disable`, `ingress.delete`.
- Ingress token: Ed25519, same wire format as relay tokens (`<base64url(json)>.<base64url(sig)>`), claims `{tenant_id, peer_id (the ingress peer), role: "ingress", exp}`; issued from the admin UI; stored on the VPS as a file.
- `GET /internal/v1/ingress/ask?domain=<fqdn>`: no token; 200 if live (any tenant), else 404. Reachable only inside the compose network; the public Caddy site never proxies `/internal/*` (explicit `respond 404` matcher).
- `GET /internal/v1/ingress/routes`: `[{service_id, label, peer_ip, target_port}]` for live services in the token's tenant; ETag; ingress polls every 10 s and keeps the last good table when the controller is unreachable.
- ACL: when live, generated rule `tag:ingress -> <peer>:<target_port>` (marked generated, not editable in the HCL editor); removed when not live. Enforcement is L3 only.
- Upstream request headers: `X-Forwarded-For`, `X-Forwarded-Proto: https`, `X-Forwarded-Host`, `X-Bamboo-Ingress-Service: <service id>`.
- `bamboo-ingress`: `apps/ingress`, AGPLv3, host network, listens on the docker bridge gateway `:8090`. Config: controller URL, service token path, public suffix `svc.miilink.net`.
- Routing: exact `Host` match; unknown host → 404; service gone → 502 with a short body.
- Limits: per service 120 req/min, per client IP 300 req/min; request body ≤ 8 MiB; upstream dial timeout 5 s; idle stream timeout 10 min. Streaming via `httputil.ReverseProxy`, no buffering (SSE, chunked).
- Telemetry per request `{service_id, client_ip, status, bytes_in, bytes_out, duration}` to ClickHouse `connection_events`; never request/response bodies, never `Authorization`.
- `bamboo-ingress` serves `/healthz`, `/version`, Prometheus `/metrics`.
- Caddy: site `*.svc.miilink.net` with `tls { on_demand }`; global `on_demand_tls { ask http://controller:8081/internal/v1/ingress/ask }`; `reverse_proxy host.docker.internal:8090` (`extra_hosts: host-gateway`) with `flush_interval -1`. Existing control-plane and relay sites keep their behavior.
- Never break the existing control plane/relay ("never break userspace"). All existing tests keep passing. Migrations are additive.
- Repo conventions: English code/docs; SPDX header `// SPDX-License-Identifier: AGPL-3.0-or-later` on every Go/TS file under `apps/`; conventional commits ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Decisions this plan makes where the spec is silent (review before executing)

1. **`approved_by` is not a foreign key** (like `audit_log.actor_id`), so deleting the approving admin never silently un-approves. Approvals made without a user identity (dev-fallback auth, API token without creator) store `uuid.Nil`.
2. **Permissions:** create = any authenticated tenant member; approve, PATCH, DELETE, token issue = admin (`requireAdmin`).
3. **Port change audit:** a `target_port` change writes `ingress.update` (spec requires every mutation be audited but names no action). Token issue writes `ingress.token.issue`.
4. **Token rotation:** table `ingress_tokens` (one row per tenant, current `jti`); the token carries `jti` and `iat` in addition to the spec claims; verification rejects any `jti` that is not current. Signed with a dedicated key `BAMBOO_INGRESS_SIGNING_KEY` (Ed25519 seed, same format as `BAMBOO_RELAY_SIGNING_KEY`). Unset ⇒ token/internal routes return 503; nothing else changes.
5. **Token issue endpoint:** `POST /api/v1/ingress/token {peer_id}` (admin). The spec says "issued from the admin UI" without naming a route.
6. **Telemetry transport:** the ingress process cannot reach ClickHouse (internal compose network), so it batches records to `POST /internal/v1/ingress/events` (token auth); the controller writes `connection_events` rows with `event_type = 'ingress_request'` and four new additive columns (`service_id`, `client_ip`, `http_status`, `duration_ms`).
7. **Controller reachability from the host network:** the controller service publishes `127.0.0.1:8081:8081` (loopback only) so the host-network ingress can reach `/internal/*`. This is one extra line on an existing compose service.
8. **Generated ACL rules** are computed at register time (never stored in HCL): a forward rule `tag:ingress -> cidr:<ip>/32:<port>` and a return rule `cidr:<ip>/32 -> tag:ingress:<port>` (WireGuard needs the ingress IP in the target's AllowedIPs). They are prepended so an approved service works, and they are only added when the tenant already has ≥1 rule; a tenant with no rules is full-mesh already and must not be switched to default-deny.
9. **Rate limiter is copied** into `apps/ingress/internal/ratelimit`: Go's `internal/` rule forbids importing `apps/controller/internal/ratelimit` from another module.
10. **`/healthz`, `/version`, `/metrics` live on a separate admin listener** (`127.0.0.1:8091`) so they never shadow a public service's paths.
11. **Route removal cancels in-flight requests** of that service, so "disable stops it within 10 s" also holds for open SSE streams.
12. **The `ingress` compose service sits behind `profiles: ["ingress"]`**, so the existing `deploy.sh` and `docker compose up` behave exactly as today until an operator opts in.
13. **Token file path is `/etc/bamboo-ingress/ingress.token`** (owner 65532, mode 0400): `/etc/bamboo` is the CLI's 0700 env directory and must not be loosened.
14. **`ask` requires `BAMBOO_INGRESS_SUFFIX` on the controller** and accepts exactly one label in front of it; with no suffix it answers 404 for everything.

---

## File Structure

Controller (`apps/controller`):

| File | Action | Responsibility |
| --- | --- | --- |
| `migrations/00024_ingress_services.sql` | Create | Tables `ingress_services`, `ingress_tokens`; RLS; `SECURITY DEFINER` functions `ingress_label_taken`, `ingress_label_live` |
| `internal/db/repo/ingress.go` | Create | `IngressServices` repo, validation, live routes |
| `internal/db/repo/ingress_tokens.go` | Create | `IngressTokens` repo (current jti per tenant) |
| `internal/db/repo/ingress_test.go` | Create | Postgres tests incl. RLS + definer functions |
| `internal/policy/generated.go` (+`_test`) | Create | `IngressRules`, `WithIngressRules` |
| `internal/handlers/coordinator_ingress.go` (+`_test`) | Create | Load live routes and merge generated rules |
| `internal/handlers/coordinator.go` | Modify (~line 653) | One call to `withIngressRules` in Register |
| `internal/config/config.go` (+`_test`) | Modify | `IngressConfig{SigningKey, Suffix}` + env |
| `internal/server/http.go` | Modify | Fields, `SetIngressConfig`, mount `/internal/v1/ingress/` |
| `internal/server/server.go` | Modify | Parse key, call `SetIngressConfig` |
| `internal/server/api.go` | Modify (routeAPI switch) | Dispatch `routeIngress` |
| `internal/server/api_router.go` | Modify (routePeersSub) | `/{id}/ingress` branch |
| `internal/server/api_ingress.go` | Create | Admin REST handlers |
| `internal/server/api_ingress_token.go` | Create | Token issue handler |
| `internal/server/api_ingress_internal.go` (+`_test`) | Create | ask, routes, events, ingress token auth |
| `internal/auth/ingress_token.go` (+`_test`) | Create | Issue/verify ingress tokens |
| `internal/clickhouse/clickhouse.go` | Modify | Additive `ALTER TABLE … ADD COLUMN IF NOT EXISTS` |
| `internal/clickhouse/connections_ingress.go` (+`_test`) | Create | `InsertIngressBatch` |
| `test/e2e/rest_ingress_test.go`, `test/e2e/internal_ingress_test.go` | Create | Handler tests against Postgres |
| `test/e2e/router_contract_test.go`, `web_route_contract_test.go` | Modify | Pin new routes |

Ingress (`apps/ingress`, new module): `go.mod`, `go.sum`, `Dockerfile`, `cmd/bamboo-ingress/{main.go,config.go,config_test.go}`, `internal/routes/{table.go,poller.go,*_test.go}`, `internal/ratelimit/{ratelimit.go,ratelimit_test.go}`, `internal/proxy/{proxy.go,idle.go,proxy_test.go}`, `internal/telemetry/{sink.go,sink_test.go}`, `internal/metrics/{metrics.go,metrics_test.go}`.

Repo root: `go.work` (add module), `Makefile` (build target), `.github/workflows/images.yml` (ingress image).

Web (`apps/web`): `src/lib/ingress.ts` (+test), `src/lib/ingressActions.ts`, `src/lib/api.ts` (add `fetchIngress`), `src/components/IngressView.tsx` (+test), `src/app/[locale]/ingress/page.tsx`, `src/app/[locale]/settings/page.tsx` (card), `messages/en.json`, `messages/zh-TW.json`.

Infra/docs: `infra/full/{docker-compose.yml,Caddyfile,.env.example,deploy.sh}`, `docs/deployment/public-services-ingress.md`.

## How tests run (CI parity)

CI (`.github/workflows/ci.yml` job `test`) starts Postgres 16 + ClickHouse 24.8, runs `goose -dir apps/controller/migrations postgres "$DATABASE_URL_TEST" up`, then `make test` (`go test -race ./...` in every directory with a `go.mod`, so `apps/ingress` is picked up automatically). Web job: `npm ci && npm run lint && npm run typecheck && npm run test && npm run build` in `apps/web`. Lint: `make lint` (golangci-lint v2.0.2). License: `make license-check`.

Local equivalent (run once per session):

```bash
cd /Users/hanfourhuang/Projects/bamboo
make dev                                   # Postgres on :15432
export DATABASE_URL_TEST='postgres://bamboo:dev@localhost:15432/bamboo?sslmode=disable'
go install github.com/pressly/goose/v3/cmd/goose@v3.27.1
$(go env GOPATH)/bin/goose -dir apps/controller/migrations postgres "$DATABASE_URL_TEST" up
# optional, for ClickHouse tests:
docker run -d --name bamboo-ch-test -p 19000:9000 -e CLICKHOUSE_DB=bamboo -e CLICKHOUSE_USER=bamboo -e CLICKHOUSE_PASSWORD=dev clickhouse/clickhouse-server:24.8-alpine
export CLICKHOUSE_URL_TEST='clickhouse://bamboo:dev@localhost:19000/bamboo'
```

Without `DATABASE_URL_TEST` the Postgres tests print `SKIP`; a step that says "Expected: PASS" for a DB test means PASS with the variable set.

---

### Task 1: Migration, repository layer, RLS bootstrap lookups

**Files:**
- Create: `apps/controller/migrations/00024_ingress_services.sql`
- Create: `apps/controller/internal/db/repo/ingress.go`
- Create: `apps/controller/internal/db/repo/ingress_tokens.go`
- Test: `apps/controller/internal/db/repo/ingress_test.go`

**Interfaces:**
- Consumes: `db.Querier`, `db.Open`, `db.WithTenant` (`internal/db/querier.go`), `repo.ErrNotFound`, `repo.NewTenants(...).Create(ctx, name, slug, ipPool)`, `requireDB(t)` (`internal/db/repo/tenants_test.go`).
- Produces (package `repo`):
  - `type IngressService struct { ID, TenantID, PeerID uuid.UUID; Label string; TargetPort int; Enabled bool; ApprovedBy *uuid.UUID; ApprovedAt *time.Time; CreatedBy *uuid.UUID; CreatedAt, UpdatedAt time.Time; PeerHostname, PeerIP, PeerApprovalStatus string }`
  - `func (s *IngressService) Approved() bool`, `func (s *IngressService) Live() bool`
  - `type IngressRoute struct { ServiceID uuid.UUID; Label string; PeerID uuid.UUID; PeerIP string; TargetPort int }`
  - `var ErrIngressLabelTaken, ErrIngressLabelInvalid, ErrIngressLabelReserved, ErrIngressPortInvalid error`
  - `func ValidateIngressLabel(label string) error`, `func ValidateIngressPort(port int) error`
  - `func NewIngressServices(q db.Querier) *IngressServices` with methods
    `Create(ctx, tenantID, peerID uuid.UUID, label string, targetPort int, createdBy *uuid.UUID) (*IngressService, error)`,
    `GetByID(ctx, id uuid.UUID) (*IngressService, error)`,
    `ListByTenant(ctx, tenantID uuid.UUID) ([]*IngressService, error)`,
    `Approve(ctx, id, approver uuid.UUID) (int64, error)`,
    `SetEnabled(ctx, id uuid.UUID, enabled bool) (int64, error)`,
    `SetTargetPort(ctx, id uuid.UUID, port int) (int64, error)` (resets approval),
    `Delete(ctx, id uuid.UUID) (int64, error)`,
    `ListLiveRoutes(ctx, tenantID uuid.UUID) ([]IngressRoute, error)`,
    `LabelTaken(ctx, label string) (bool, error)` (definer function, any tenant),
    `LabelLive(ctx, label string) (bool, error)` (definer function, any tenant)
  - `type IngressToken struct { TenantID, PeerID, JTI uuid.UUID; IssuedBy *uuid.UUID; IssuedAt, ExpiresAt time.Time }`
  - `func NewIngressTokens(q db.Querier) *IngressTokens` with `Put(ctx, t IngressToken) error`, `Get(ctx, tenantID uuid.UUID) (*IngressToken, error)` (`ErrNotFound` when absent)

- [ ] **Step 1: Write the migration**

`00023_peer_presence.sql` is the latest migration, so this is `00024`.

```sql
-- +goose Up
-- +goose StatementBegin

-- Public services ingress (docs/design/2026-11-02-public-services-ingress.md).
-- Additive only: two new tables and two SECURITY DEFINER lookups.
--
-- approved_by is the approving admin's user id; NULL means pending. It is
-- not a foreign key (same as audit_log.actor_id) so deleting that admin
-- never silently un-approves a service. uuid.Nil records an approval made
-- without a user identity (dev-fallback auth, API token without creator).
CREATE TABLE IF NOT EXISTS ingress_services (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    peer_id      UUID        NOT NULL REFERENCES peers(id)   ON DELETE CASCADE,
    label        TEXT        NOT NULL,
    target_port  INTEGER     NOT NULL,
    enabled      BOOLEAN     NOT NULL DEFAULT false,
    approved_by  UUID,
    approved_at  TIMESTAMPTZ,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Global, not per tenant: the label is a public DNS name.
    CONSTRAINT ingress_services_label_unique UNIQUE (label),
    CONSTRAINT ingress_services_label_format
        CHECK (label ~ '^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$'),
    CONSTRAINT ingress_services_label_reserved
        CHECK (label NOT IN ('www', 'api', 'admin', 'relay', 'controller', 'ingress', 'mail')),
    CONSTRAINT ingress_services_port_range
        CHECK (target_port BETWEEN 1024 AND 65535)
);
CREATE INDEX IF NOT EXISTS ingress_services_tenant_idx ON ingress_services (tenant_id);
CREATE INDEX IF NOT EXISTS ingress_services_peer_idx   ON ingress_services (peer_id);

-- One current ingress service token per tenant. Issuing a new token
-- replaces jti, which revokes the previous token.
CREATE TABLE IF NOT EXISTS ingress_tokens (
    tenant_id   UUID        PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    peer_id     UUID        NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    jti         UUID        NOT NULL,
    issued_by   UUID,
    issued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

GRANT SELECT, INSERT, UPDATE, DELETE ON ingress_services, ingress_tokens
    TO bamboo_app, bamboo_maintenance;

ALTER TABLE ingress_services ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingress_services FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ingress_services;
CREATE POLICY tenant_isolation ON ingress_services
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE ingress_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingress_tokens FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ingress_tokens;
CREATE POLICY tenant_isolation ON ingress_tokens
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- RLS bootstrap lookups (ADR 0014, project-goals P0 item 2). Labels are
-- unique across tenants, so "is this label taken" and "is this name live"
-- cannot run under a tenant GUC. Each function returns one boolean for a
-- presented label and nothing else. Owned by bamboo_maintenance
-- (BYPASSRLS, NOLOGIN) rather than the superuser migration role, so the
-- bypass is no wider than that role already is.
CREATE OR REPLACE FUNCTION ingress_label_taken(p_label TEXT) RETURNS BOOLEAN
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = public, pg_temp
AS $fn$
    SELECT EXISTS (SELECT 1 FROM ingress_services WHERE label = p_label);
$fn$;

CREATE OR REPLACE FUNCTION ingress_label_live(p_label TEXT) RETURNS BOOLEAN
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = public, pg_temp
AS $fn$
    SELECT EXISTS (
        SELECT 1
          FROM ingress_services s
          JOIN peers p ON p.id = s.peer_id
         WHERE s.label = p_label
           AND s.enabled
           AND s.approved_by IS NOT NULL
           AND p.approval_status = 'approved'
    );
$fn$;

ALTER FUNCTION ingress_label_taken(TEXT) OWNER TO bamboo_maintenance;
ALTER FUNCTION ingress_label_live(TEXT)  OWNER TO bamboo_maintenance;
REVOKE ALL ON FUNCTION ingress_label_taken(TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION ingress_label_live(TEXT)  FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ingress_label_taken(TEXT) TO bamboo_app, bamboo_maintenance;
GRANT EXECUTE ON FUNCTION ingress_label_live(TEXT)  TO bamboo_app, bamboo_maintenance;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS ingress_label_live(TEXT);
DROP FUNCTION IF EXISTS ingress_label_taken(TEXT);
DROP TABLE IF EXISTS ingress_tokens;
DROP TABLE IF EXISTS ingress_services;
-- +goose StatementEnd
```

- [ ] **Step 2: Verify the migration round-trips (manual; not an automated test, because down-migrating a shared test DB would break concurrently running packages)**

Run:
```bash
cd /Users/hanfourhuang/Projects/bamboo
G=$(go env GOPATH)/bin/goose
$G -dir apps/controller/migrations postgres "$DATABASE_URL_TEST" up
$G -dir apps/controller/migrations postgres "$DATABASE_URL_TEST" down
$G -dir apps/controller/migrations postgres "$DATABASE_URL_TEST" up
$G -dir apps/controller/migrations postgres "$DATABASE_URL_TEST" status | tail -2
psql "$DATABASE_URL_TEST" -c "SELECT proname, pg_get_userbyid(proowner), prosecdef FROM pg_proc WHERE proname LIKE 'ingress_label_%' ORDER BY 1"
```
Expected: `OK   00024_ingress_services.sql` on up, `OK` on down, last status line shows `00024_ingress_services.sql` applied; the query prints two rows owned by `bamboo_maintenance` with `prosecdef = t`.

- [ ] **Step 3: Write the failing repository tests**

`apps/controller/internal/db/repo/ingress_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package repo_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// requireAppDB opens a pool as bamboo_app (RLS applies), the role the
// controller runs as. requireDB's maintenance pool bypasses RLS, so RLS
// assertions must use this one.
func requireAppDB(t *testing.T) *db.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type ingressFixture struct {
	tenantID uuid.UUID
	peerID   uuid.UUID
	peerIP   string
}

// seedIngressTenant creates a tenant with one approved peer, using the
// maintenance pool. ipOctet keeps peer IPs distinct per test.
func seedIngressTenant(t *testing.T, admin *db.Pool, ipOctet int) ingressFixture {
	t.Helper()
	ctx := context.Background()
	slug := fmt.Sprintf("ingress-%s", uuid.NewString()[:8])
	tenant, err := repo.NewTenants(admin).Create(ctx, "Ingress", slug, fmt.Sprintf("100.64.%d.0/24", ipOctet))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant.ID) })
	ip := fmt.Sprintf("100.64.%d.5", ipOctet)
	var peerID uuid.UUID
	if err := admin.QueryRow(ctx, `
		INSERT INTO peers (tenant_id, hostname, wireguard_public_key, ip, approval_status)
		VALUES ($1, 'mac', $2, $3, 'approved') RETURNING id`,
		tenant.ID, uuid.NewString(), ip).Scan(&peerID); err != nil {
		t.Fatalf("insert peer: %v", err)
	}
	return ingressFixture{tenantID: tenant.ID, peerID: peerID, peerIP: ip}
}

func randomLabel() string { return "svc-" + uuid.NewString()[:8] }

func TestValidateIngressLabel(t *testing.T) {
	cases := map[string]error{
		"yeschef":    nil,
		"a":          nil,
		"a-1":        nil,
		"0123456789012345678901234567890a": nil, // 32 chars
		"0123456789012345678901234567890ab": repo.ErrIngressLabelInvalid, // 33
		"-bad":       repo.ErrIngressLabelInvalid,
		"bad-":       repo.ErrIngressLabelInvalid,
		"Bad":        repo.ErrIngressLabelInvalid,
		"has.dot":    repo.ErrIngressLabelInvalid,
		"":           repo.ErrIngressLabelInvalid,
		"www":        repo.ErrIngressLabelReserved,
		"ingress":    repo.ErrIngressLabelReserved,
		"controller": repo.ErrIngressLabelReserved,
	}
	for label, want := range cases {
		if got := repo.ValidateIngressLabel(label); !errors.Is(got, want) && got != want {
			t.Errorf("ValidateIngressLabel(%q) = %v, want %v", label, got, want)
		}
	}
	for _, p := range []int{1023, 65536, 0} {
		if !errors.Is(repo.ValidateIngressPort(p), repo.ErrIngressPortInvalid) {
			t.Errorf("port %d accepted", p)
		}
	}
	for _, p := range []int{1024, 47800, 65535} {
		if err := repo.ValidateIngressPort(p); err != nil {
			t.Errorf("port %d rejected: %v", p, err)
		}
	}
}

func TestIngress_CreateApproveEnableIsLive(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 201)
	label := randomLabel()
	approver := uuid.New()

	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		if s.Approved() || s.Enabled || s.Live() {
			t.Errorf("new service should be pending/disabled: %+v", s)
		}
		if s.PeerIP != fx.peerIP || s.PeerHostname != "mac" {
			t.Errorf("peer join wrong: ip=%q host=%q", s.PeerIP, s.PeerHostname)
		}
		if _, err := svcs.Approve(ctx, s.ID, approver); err != nil {
			return err
		}
		if _, err := svcs.SetEnabled(ctx, s.ID, true); err != nil {
			return err
		}
		got, err := svcs.GetByID(ctx, s.ID)
		if err != nil {
			return err
		}
		if !got.Live() || got.ApprovedBy == nil || *got.ApprovedBy != approver || got.ApprovedAt == nil {
			t.Errorf("want live+approved by %s, got %+v", approver, got)
		}
		routes, err := svcs.ListLiveRoutes(ctx, fx.tenantID)
		if err != nil {
			return err
		}
		if len(routes) != 1 || routes[0].Label != label || routes[0].PeerIP != fx.peerIP || routes[0].TargetPort != 47800 {
			t.Errorf("live routes = %+v", routes)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func TestIngress_SetTargetPortResetsApproval(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 202)
	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, randomLabel(), 47800, nil)
		if err != nil {
			return err
		}
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		if _, err := svcs.SetEnabled(ctx, s.ID, true); err != nil {
			return err
		}
		if _, err := svcs.SetTargetPort(ctx, s.ID, 47801); err != nil {
			return err
		}
		got, err := svcs.GetByID(ctx, s.ID)
		if err != nil {
			return err
		}
		if got.TargetPort != 47801 || got.Approved() || got.ApprovedAt != nil || got.Live() {
			t.Errorf("port change must reset approval: %+v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func TestIngress_PeerNotApprovedIsNotLive(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 203)
	label := randomLabel()
	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		s, err := svcs.Create(ctx, fx.tenantID, fx.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		_, err = svcs.SetEnabled(ctx, s.ID, true)
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE peers SET approval_status='pending' WHERE id=$1`, fx.peerID); err != nil {
		t.Fatalf("unapprove peer: %v", err)
	}
	live, err := repo.NewIngressServices(app).LabelLive(ctx, label)
	if err != nil {
		t.Fatalf("LabelLive: %v", err)
	}
	if live {
		t.Error("service on a pending peer must not be live")
	}
}

// TestIngress_LabelUniqueAcrossTenants pins the cross-tenant rule and the
// SECURITY DEFINER lookups: both run on the app pool with no tenant GUC.
func TestIngress_LabelUniqueAcrossTenants(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	a := seedIngressTenant(t, admin, 204)
	b := seedIngressTenant(t, admin, 205)
	label := randomLabel()

	if err := db.WithTenant(ctx, app, a.tenantID, func(q db.Querier) error {
		s, err := repo.NewIngressServices(q).Create(ctx, a.tenantID, a.peerID, label, 47800, nil)
		if err != nil {
			return err
		}
		svcs := repo.NewIngressServices(q)
		if _, err := svcs.Approve(ctx, s.ID, uuid.New()); err != nil {
			return err
		}
		_, err = svcs.SetEnabled(ctx, s.ID, true)
		return err
	}); err != nil {
		t.Fatalf("create in A: %v", err)
	}

	unscoped := repo.NewIngressServices(app)
	taken, err := unscoped.LabelTaken(ctx, label)
	if err != nil || !taken {
		t.Fatalf("LabelTaken = %v, %v; want true from a GUC-less app pool", taken, err)
	}
	live, err := unscoped.LabelLive(ctx, label)
	if err != nil || !live {
		t.Fatalf("LabelLive = %v, %v; want true", live, err)
	}
	if other, _ := unscoped.LabelLive(ctx, randomLabel()); other {
		t.Error("unknown label reported live")
	}

	err = db.WithTenant(ctx, app, b.tenantID, func(q db.Querier) error {
		_, err := repo.NewIngressServices(q).Create(ctx, b.tenantID, b.peerID, label, 47800, nil)
		return err
	})
	if !errors.Is(err, repo.ErrIngressLabelTaken) {
		t.Fatalf("create same label in B: err=%v, want ErrIngressLabelTaken", err)
	}
}

func TestIngress_RLSConfinesToTenant(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	a := seedIngressTenant(t, admin, 206)
	b := seedIngressTenant(t, admin, 207)

	var bService uuid.UUID
	if err := db.WithTenant(ctx, app, b.tenantID, func(q db.Querier) error {
		s, err := repo.NewIngressServices(q).Create(ctx, b.tenantID, b.peerID, randomLabel(), 47800, nil)
		if err == nil {
			bService = s.ID
		}
		return err
	}); err != nil {
		t.Fatalf("create in B: %v", err)
	}

	err := db.WithTenant(ctx, app, a.tenantID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		list, err := svcs.ListByTenant(ctx, b.tenantID)
		if err != nil {
			return err
		}
		if len(list) != 0 {
			t.Errorf("tenant A listed %d of B's services", len(list))
		}
		if _, err := svcs.GetByID(ctx, bService); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("tenant A GetByID(B's service) err=%v, want ErrNotFound", err)
		}
		if n, err := svcs.SetEnabled(ctx, bService, true); err != nil || n != 0 {
			t.Errorf("tenant A modified B's row: n=%d err=%v", n, err)
		}
		if n, err := svcs.Delete(ctx, bService); err != nil || n != 0 {
			t.Errorf("tenant A deleted B's row: n=%d err=%v", n, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx A: %v", err)
	}

	err = db.WithTenant(ctx, app, a.tenantID, func(q db.Querier) error {
		_, err := repo.NewIngressServices(q).Create(ctx, b.tenantID, b.peerID, randomLabel(), 47800, nil)
		return err
	})
	if err == nil {
		t.Fatal("WITH CHECK accepted an insert for tenant B under tenant A's GUC")
	}
}

func TestIngressTokens_PutGetReplace(t *testing.T) {
	admin := requireDB(t)
	app := requireAppDB(t)
	ctx := context.Background()
	fx := seedIngressTenant(t, admin, 208)
	first, second := uuid.New(), uuid.New()
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	err := db.WithTenant(ctx, app, fx.tenantID, func(q db.Querier) error {
		toks := repo.NewIngressTokens(q)
		if _, err := toks.Get(ctx, fx.tenantID); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("Get before Put: %v, want ErrNotFound", err)
		}
		for _, jti := range []uuid.UUID{first, second} {
			if err := toks.Put(ctx, repo.IngressToken{TenantID: fx.tenantID, PeerID: fx.peerID, JTI: jti, ExpiresAt: exp}); err != nil {
				return err
			}
		}
		got, err := toks.Get(ctx, fx.tenantID)
		if err != nil {
			return err
		}
		if got.JTI != second || got.PeerID != fx.peerID || !got.ExpiresAt.Equal(exp) {
			t.Errorf("Get = %+v, want jti %s", got, second)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/controller && go test ./internal/db/repo/ -run 'Ingress' -count=1`
Expected: FAIL to compile with `undefined: repo.ValidateIngressLabel` (and the other new symbols).

- [ ] **Step 5: Implement `ingress.go`**

`apps/controller/internal/db/repo/ingress.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
)

// IngressService is one ingress_services row joined with the peer fields
// liveness and the admin list need. See
// docs/design/2026-11-02-public-services-ingress.md §4.1.
type IngressService struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	PeerID     uuid.UUID
	Label      string
	TargetPort int
	Enabled    bool
	ApprovedBy *uuid.UUID // nil = pending
	ApprovedAt *time.Time
	CreatedBy  *uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time

	PeerHostname       string
	PeerIP             string
	PeerApprovalStatus string
}

// Approved reports whether an admin has approved the service.
func (s *IngressService) Approved() bool { return s.ApprovedBy != nil }

// Live reports whether the public name should route: enabled, approved,
// and on an approved peer. A deleted peer cascades the row away.
func (s *IngressService) Live() bool {
	return s.Enabled && s.ApprovedBy != nil && s.PeerApprovalStatus == "approved"
}

// IngressRoute is one live service as the ingress proxy and the ACL
// generator see it.
type IngressRoute struct {
	ServiceID  uuid.UUID
	Label      string
	PeerID     uuid.UUID
	PeerIP     string
	TargetPort int
}

var (
	ErrIngressLabelTaken    = errors.New("ingress label is already in use")
	ErrIngressLabelInvalid  = errors.New("label must be 1-32 lowercase letters, digits, or inner hyphens")
	ErrIngressLabelReserved = errors.New("label is reserved")
	ErrIngressPortInvalid   = errors.New("target_port must be between 1024 and 65535")
)

var ingressLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

var ingressReservedLabels = map[string]struct{}{
	"www": {}, "api": {}, "admin": {}, "relay": {}, "controller": {}, "ingress": {}, "mail": {},
}

// ValidateIngressLabel mirrors the migration's CHECK constraints so the
// REST layer can answer 400 before touching the database.
func ValidateIngressLabel(label string) error {
	if !ingressLabelRE.MatchString(label) {
		return ErrIngressLabelInvalid
	}
	if _, reserved := ingressReservedLabels[label]; reserved {
		return ErrIngressLabelReserved
	}
	return nil
}

// ValidateIngressPort mirrors ingress_services_port_range.
func ValidateIngressPort(port int) error {
	if port < 1024 || port > 65535 {
		return ErrIngressPortInvalid
	}
	return nil
}

// IngressServices is the repository for ingress_services.
type IngressServices struct {
	q db.Querier
}

// NewIngressServices constructs the repository.
func NewIngressServices(q db.Querier) *IngressServices { return &IngressServices{q: q} }

const ingressSelect = `
	SELECT s.id, s.tenant_id, s.peer_id, s.label, s.target_port, s.enabled,
	       s.approved_by, s.approved_at, s.created_by, s.created_at, s.updated_at,
	       p.hostname, host(p.ip), p.approval_status
	  FROM ingress_services s
	  JOIN peers p ON p.id = s.peer_id`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIngress(row rowScanner) (*IngressService, error) {
	var s IngressService
	err := row.Scan(
		&s.ID, &s.TenantID, &s.PeerID, &s.Label, &s.TargetPort, &s.Enabled,
		&s.ApprovedBy, &s.ApprovedAt, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt,
		&s.PeerHostname, &s.PeerIP, &s.PeerApprovalStatus,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Create inserts a pending, disabled service and returns it joined with
// its peer. A label used by any tenant yields ErrIngressLabelTaken; the
// enclosing transaction is then aborted, so callers return the error.
func (r *IngressServices) Create(ctx context.Context, tenantID, peerID uuid.UUID, label string, targetPort int, createdBy *uuid.UUID) (*IngressService, error) {
	if err := ValidateIngressLabel(label); err != nil {
		return nil, err
	}
	if err := ValidateIngressPort(targetPort); err != nil {
		return nil, err
	}
	var id uuid.UUID
	err := r.q.QueryRow(ctx, `
		INSERT INTO ingress_services (tenant_id, peer_id, label, target_port, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`, tenantID, peerID, label, targetPort, createdBy).Scan(&id)
	if isUniqueViolation(err, "ingress_services_label_unique") {
		return nil, ErrIngressLabelTaken
	}
	if err != nil {
		return nil, fmt.Errorf("insert ingress service: %w", err)
	}
	return r.GetByID(ctx, id)
}

// GetByID returns one service, or ErrNotFound (also when RLS hides it).
func (r *IngressServices) GetByID(ctx context.Context, id uuid.UUID) (*IngressService, error) {
	s, err := scanIngress(r.q.QueryRow(ctx, ingressSelect+` WHERE s.id = $1`, id))
	if err != nil {
		return nil, asNotFound(err)
	}
	return s, nil
}

// ListByTenant returns the tenant's services ordered by label.
func (r *IngressServices) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*IngressService, error) {
	rows, err := r.q.Query(ctx, ingressSelect+` WHERE s.tenant_id = $1 ORDER BY s.label`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list ingress services: %w", err)
	}
	defer rows.Close()
	out := []*IngressService{}
	for rows.Next() {
		s, err := scanIngress(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ingress service: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Approve records the approver. Returns rows affected.
func (r *IngressServices) Approve(ctx context.Context, id, approver uuid.UUID) (int64, error) {
	return r.exec(ctx, `UPDATE ingress_services
		SET approved_by = $2, approved_at = now(), updated_at = now() WHERE id = $1`, id, approver)
}

// SetEnabled flips enabled. Returns rows affected.
func (r *IngressServices) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) (int64, error) {
	return r.exec(ctx, `UPDATE ingress_services
		SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
}

// SetTargetPort changes the port and resets approval (spec §4.2).
func (r *IngressServices) SetTargetPort(ctx context.Context, id uuid.UUID, port int) (int64, error) {
	if err := ValidateIngressPort(port); err != nil {
		return 0, err
	}
	return r.exec(ctx, `UPDATE ingress_services
		SET target_port = $2, approved_by = NULL, approved_at = NULL, updated_at = now()
		WHERE id = $1`, id, port)
}

// Delete removes the row. Returns rows affected.
func (r *IngressServices) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	return r.exec(ctx, `DELETE FROM ingress_services WHERE id = $1`, id)
}

func (r *IngressServices) exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := r.q.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("ingress services: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListLiveRoutes returns the tenant's live services. The WHERE clause is
// the same liveness rule as IngressService.Live and ingress_label_live.
func (r *IngressServices) ListLiveRoutes(ctx context.Context, tenantID uuid.UUID) ([]IngressRoute, error) {
	rows, err := r.q.Query(ctx, `
		SELECT s.id, s.label, s.peer_id, host(p.ip), s.target_port
		  FROM ingress_services s
		  JOIN peers p ON p.id = s.peer_id
		 WHERE s.tenant_id = $1
		   AND s.enabled AND s.approved_by IS NOT NULL
		   AND p.approval_status = 'approved'
		 ORDER BY s.label`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list live ingress routes: %w", err)
	}
	defer rows.Close()
	out := []IngressRoute{}
	for rows.Next() {
		var rt IngressRoute
		if err := rows.Scan(&rt.ServiceID, &rt.Label, &rt.PeerID, &rt.PeerIP, &rt.TargetPort); err != nil {
			return nil, fmt.Errorf("scan live route: %w", err)
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

// LabelTaken asks the SECURITY DEFINER bootstrap function whether any
// tenant uses label. RLS bootstrap: safe without a tenant GUC.
func (r *IngressServices) LabelTaken(ctx context.Context, label string) (bool, error) {
	var taken bool
	if err := r.q.QueryRow(ctx, `SELECT ingress_label_taken($1)`, label).Scan(&taken); err != nil {
		return false, fmt.Errorf("ingress_label_taken: %w", err)
	}
	return taken, nil
}

// LabelLive asks the SECURITY DEFINER bootstrap function whether label is
// live in any tenant. RLS bootstrap: used by Caddy's on-demand TLS ask.
func (r *IngressServices) LabelLive(ctx context.Context, label string) (bool, error) {
	var live bool
	if err := r.q.QueryRow(ctx, `SELECT ingress_label_live($1)`, label).Scan(&live); err != nil {
		return false, fmt.Errorf("ingress_label_live: %w", err)
	}
	return live, nil
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
```

- [ ] **Step 6: Implement `ingress_tokens.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
)

// IngressToken is the tenant's current ingress service token. Only the
// jti is stored; the signed token itself is shown once and never kept.
type IngressToken struct {
	TenantID  uuid.UUID
	PeerID    uuid.UUID
	JTI       uuid.UUID
	IssuedBy  *uuid.UUID
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// IngressTokens is the repository for ingress_tokens.
type IngressTokens struct {
	q db.Querier
}

// NewIngressTokens constructs the repository.
func NewIngressTokens(q db.Querier) *IngressTokens { return &IngressTokens{q: q} }

// Put stores t as the tenant's current token, replacing (revoking) any
// previous one.
func (r *IngressTokens) Put(ctx context.Context, t IngressToken) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO ingress_tokens (tenant_id, peer_id, jti, issued_by, issued_at, expires_at)
		VALUES ($1, $2, $3, $4, now(), $5)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    peer_id = EXCLUDED.peer_id, jti = EXCLUDED.jti, issued_by = EXCLUDED.issued_by,
		    issued_at = now(), expires_at = EXCLUDED.expires_at`,
		t.TenantID, t.PeerID, t.JTI, t.IssuedBy, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("put ingress token: %w", err)
	}
	return nil
}

// Get returns the tenant's current token, or ErrNotFound.
func (r *IngressTokens) Get(ctx context.Context, tenantID uuid.UUID) (*IngressToken, error) {
	var t IngressToken
	err := r.q.QueryRow(ctx, `
		SELECT tenant_id, peer_id, jti, issued_by, issued_at, expires_at
		  FROM ingress_tokens WHERE tenant_id = $1`, tenantID).Scan(
		&t.TenantID, &t.PeerID, &t.JTI, &t.IssuedBy, &t.IssuedAt, &t.ExpiresAt)
	if err != nil {
		return nil, asNotFound(err)
	}
	return &t, nil
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/controller && go test -race ./internal/db/repo/ -run 'Ingress' -count=1 -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)'`
Expected: every `--- PASS` (7 tests), final `ok  github.com/hanfour/bamboo/apps/controller/internal/db/repo`.

Then the whole package (nothing else may regress): `go test -race ./internal/db/... -count=1` → `ok` lines only.

- [ ] **Step 8: Commit**

```bash
cd /Users/hanfourhuang/Projects/bamboo
git add apps/controller/migrations/00024_ingress_services.sql apps/controller/internal/db/repo/ingress.go apps/controller/internal/db/repo/ingress_tokens.go apps/controller/internal/db/repo/ingress_test.go
git commit -m "feat(controller): ingress_services table, repo, and RLS bootstrap lookups

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Generated ACL rules for live services

**Files:**
- Create: `apps/controller/internal/policy/generated.go`
- Test: `apps/controller/internal/policy/generated_test.go`
- Create: `apps/controller/internal/handlers/coordinator_ingress.go`
- Test: `apps/controller/internal/handlers/coordinator_ingress_test.go`
- Modify: `apps/controller/internal/handlers/coordinator.go` (Register, right after `policyDoc, currentRev := h.loadPolicyAndRevision(ctx, tenant.ID)`, ~line 653)

**Interfaces:**
- Consumes: `repo.NewIngressServices(q).ListLiveRoutes(ctx, tenantID) ([]repo.IngressRoute, error)` (Task 1); `policy.Policy`, `policy.Rule`, `policy.Matcher`, `policy.Allow`; `allowedIPsFor` (handlers, unexported).
- Produces:
  - `const policy.IngressTag = "ingress"` (peer tag name; DSL form `tag:ingress`)
  - `const policy.GeneratedRulePrefix = "generated:"`
  - `type policy.IngressTarget struct { ServiceID string; PeerIP netip.Addr; Port uint16 }`
  - `func policy.IngressRules(targets []IngressTarget) []Rule`
  - `func policy.WithIngressRules(p *Policy, targets []IngressTarget) *Policy` (returns a new Policy; never mutates `p`)
  - `func policy.IsGenerated(r Rule) bool`
  - `func (h *CoordinatorHandler) withIngressRules(ctx context.Context, tenantID uuid.UUID, p *policy.Policy) *policy.Policy`
  - `func ingressTargets(routes []repo.IngressRoute) []policy.IngressTarget`

Why two rules: WireGuard drops packets whose source is not in the sender's AllowedIPs, and `allowedIPsFor` is directional, so the target peer must also list the ingress peer. Why only when rules exist: `Allow` treats zero rules as full mesh; adding a rule to such a tenant would switch it to default-deny and cut every other pair.

- [ ] **Step 1: Write the failing policy tests**

`apps/controller/internal/policy/generated_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

func ingressTarget(t *testing.T) policy.IngressTarget {
	return policy.IngressTarget{ServiceID: "svc-1", PeerIP: addr(t, "100.64.0.5"), Port: 47800}
}

func TestWithIngressRules_NilAndEmptyPolicyUnchanged(t *testing.T) {
	targets := []policy.IngressTarget{ingressTarget(t)}
	if got := policy.WithIngressRules(nil, targets); got != nil {
		t.Errorf("nil policy must stay nil (full mesh), got %+v", got)
	}
	empty := &policy.Policy{}
	if got := policy.WithIngressRules(empty, targets); len(got.Rules) != 0 {
		t.Errorf("empty policy must stay rule-less (full mesh), got %d rules", len(got.Rules))
	}
}

func TestWithIngressRules_PrependsAndDoesNotMutate(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	got := policy.WithIngressRules(p, []policy.IngressTarget{ingressTarget(t)})
	if len(p.Rules) != 1 {
		t.Fatalf("input policy mutated: %d rules", len(p.Rules))
	}
	if len(got.Rules) != 3 {
		t.Fatalf("want 2 generated + 1 authored rules, got %d", len(got.Rules))
	}
	if !policy.IsGenerated(got.Rules[0]) || !policy.IsGenerated(got.Rules[1]) || policy.IsGenerated(got.Rules[2]) {
		t.Errorf("generated rules must come first and be marked: %+v", got.Rules)
	}
	if got.Rules[0].ID != "generated:ingress:svc-1" || got.Rules[1].ID != "generated:ingress:svc-1:return" {
		t.Errorf("rule ids = %q, %q", got.Rules[0].ID, got.Rules[1].ID)
	}
}

func TestWithIngressRules_AllowsBothDirectionsOnlyForTarget(t *testing.T) {
	p := mustParse(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	got := policy.WithIngressRules(p, []policy.IngressTarget{ingressTarget(t)})
	ingress := policy.PeerView{IP: addr(t, "100.64.0.9"), Tags: []string{policy.IngressTag}}
	target := policy.PeerView{IP: addr(t, "100.64.0.5")}
	other := policy.PeerView{IP: addr(t, "100.64.0.6")}

	if !policy.Allow(got, ingress, target) {
		t.Error("ingress -> target must be allowed")
	}
	if !policy.Allow(got, target, ingress) {
		t.Error("target -> ingress (return path) must be allowed")
	}
	if policy.Allow(got, ingress, other) {
		t.Error("ingress must not reach a peer without a live service")
	}
	if policy.Allow(p, ingress, target) {
		t.Error("without generated rules the authored policy denies ingress -> target")
	}
	if ok, _ := got.Evaluate(policy.EvalRequest{SrcTags: []string{policy.IngressTag}, DstIP: addr(t, "100.64.0.5"), DstPort: 22}); ok == policy.ActionAllow {
		t.Error("generated rule must cover only the target port")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/controller && go test ./internal/policy/ -run 'IngressRules' -count=1`
Expected: FAIL to compile, `undefined: policy.IngressTarget`.

- [ ] **Step 3: Implement `generated.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"net/netip"
	"strings"
)

// IngressTag is the peer tag the public services ingress peer carries
// (DSL form "tag:ingress").
const IngressTag = "ingress"

// GeneratedRulePrefix marks rules the controller synthesises. They are
// never stored in the tenant's HCL, so the ACL editor cannot edit them.
const GeneratedRulePrefix = "generated:"

// IngressTarget is one live public service: the ingress peer may reach
// PeerIP on Port.
type IngressTarget struct {
	ServiceID string
	PeerIP    netip.Addr
	Port      uint16
}

// IngressRules returns two allow rules per target: tag:ingress ->
// <peer>/32:<port>, and the return path <peer>/32 -> tag:ingress:<port>
// that WireGuard needs so the target lists the ingress peer in its
// AllowedIPs. Port enforcement beyond L3 stays with the target host.
func IngressRules(targets []IngressTarget) []Rule {
	out := make([]Rule, 0, 2*len(targets))
	for _, t := range targets {
		if !t.PeerIP.IsValid() || t.Port == 0 {
			continue
		}
		host := netip.PrefixFrom(t.PeerIP, t.PeerIP.BitLen())
		ports := PortSpec{Ports: []uint16{t.Port}}
		id := GeneratedRulePrefix + "ingress:" + t.ServiceID
		out = append(out,
			Rule{
				ID:           id,
				Action:       ActionAllow,
				Description:  "generated: public services ingress",
				Sources:      []Matcher{{Kind: MatcherTag, Name: IngressTag}},
				Destinations: []Matcher{{Kind: MatcherCIDR, CIDR: host, Ports: ports}},
			},
			Rule{
				ID:           id + ":return",
				Action:       ActionAllow,
				Description:  "generated: public services ingress return path",
				Sources:      []Matcher{{Kind: MatcherCIDR, CIDR: host}},
				Destinations: []Matcher{{Kind: MatcherTag, Name: IngressTag, Ports: ports}},
			},
		)
	}
	return out
}

// WithIngressRules returns a copy of p with the generated rules first.
// A nil or rule-less policy is returned as is: it already means full
// mesh, and adding a rule would turn on default-deny for the tenant.
func WithIngressRules(p *Policy, targets []IngressTarget) *Policy {
	if p == nil || len(p.Rules) == 0 || len(targets) == 0 {
		return p
	}
	generated := IngressRules(targets)
	rules := make([]Rule, 0, len(generated)+len(p.Rules))
	rules = append(rules, generated...)
	rules = append(rules, p.Rules...)
	return &Policy{Rules: rules, TagOwners: p.TagOwners, Groups: p.Groups}
}

// IsGenerated reports whether r was synthesised by the controller.
func IsGenerated(r Rule) bool { return strings.HasPrefix(r.ID, GeneratedRulePrefix) }
```

- [ ] **Step 4: Run the policy tests**

Run: `go test -race ./internal/policy/... -count=1`
Expected: `ok  github.com/hanfour/bamboo/apps/controller/internal/policy` (existing tests included).

- [ ] **Step 5: Write the failing handler test**

`apps/controller/internal/handlers/coordinator_ingress_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

func TestIngressTargets_SkipsBadIPs(t *testing.T) {
	sid := uuid.New()
	got := ingressTargets([]repo.IngressRoute{
		{ServiceID: sid, PeerIP: "100.64.0.5", TargetPort: 47800},
		{ServiceID: uuid.New(), PeerIP: "not-an-ip", TargetPort: 47800},
	})
	if len(got) != 1 || got[0].ServiceID != sid.String() || got[0].Port != 47800 || got[0].PeerIP.String() != "100.64.0.5" {
		t.Errorf("ingressTargets = %+v", got)
	}
}

func TestAllowedIPsFor_GeneratedIngressRulesBothDirections(t *testing.T) {
	p := mustParsePolicy(t, `
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:5432"]
}`)
	ingress := &repo.Peer{ID: uuid.New(), IP: "100.64.0.9", Tags: []string{policy.IngressTag}}
	target := &repo.Peer{ID: uuid.New(), IP: "100.64.0.5"}
	if got := allowedIPsFor(p, ingress, target, nat64EgressRoute{}); got != nil {
		t.Fatalf("before liveness: got %v, want nil", got)
	}
	live := policy.WithIngressRules(p, ingressTargets([]repo.IngressRoute{{ServiceID: uuid.New(), PeerIP: "100.64.0.5", TargetPort: 47800}}))
	if got := allowedIPsFor(live, ingress, target, nat64EgressRoute{}); len(got) == 0 || got[0] != "100.64.0.5/32" {
		t.Errorf("ingress view of target = %v, want [100.64.0.5/32]", got)
	}
	if got := allowedIPsFor(live, target, ingress, nat64EgressRoute{}); len(got) == 0 || got[0] != "100.64.0.9/32" {
		t.Errorf("target view of ingress = %v, want [100.64.0.9/32]", got)
	}
}
```

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/handlers/ -run 'IngressTargets|GeneratedIngress' -count=1`
Expected: FAIL to compile, `undefined: ingressTargets`.

- [ ] **Step 7: Implement `coordinator_ingress.go` and wire Register**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// withIngressRules adds the generated public-services rules for the
// tenant's live services (docs/design/2026-11-02-public-services-ingress.md
// §4.4). On a lookup error it logs and returns p unchanged: a failed
// lookup must not black-hole the tenant.
func (h *CoordinatorHandler) withIngressRules(ctx context.Context, tenantID uuid.UUID, p *policy.Policy) *policy.Policy {
	if p == nil || len(p.Rules) == 0 {
		return p
	}
	var routes []repo.IngressRoute
	err := db.WithTenant(ctx, h.pool, tenantID, func(q db.Querier) error {
		r, lerr := repo.NewIngressServices(q).ListLiveRoutes(ctx, tenantID)
		routes = r
		return lerr
	})
	if err != nil {
		slog.Warn("load live ingress routes", "tenant_id", tenantID, "err", err)
		return p
	}
	return policy.WithIngressRules(p, ingressTargets(routes))
}

// ingressTargets converts live routes to policy targets, skipping rows
// whose IP does not parse.
func ingressTargets(routes []repo.IngressRoute) []policy.IngressTarget {
	out := make([]policy.IngressTarget, 0, len(routes))
	for _, rt := range routes {
		addr, err := netip.ParseAddr(rt.PeerIP)
		if err != nil || rt.TargetPort < 1 || rt.TargetPort > 65535 {
			continue
		}
		out = append(out, policy.IngressTarget{ServiceID: rt.ServiceID.String(), PeerIP: addr, Port: uint16(rt.TargetPort)})
	}
	return out
}
```

In `coordinator.go` Register, change:

```go
	policyDoc, currentRev := h.loadPolicyAndRevision(ctx, tenant.ID)
```
to:
```go
	policyDoc, currentRev := h.loadPolicyAndRevision(ctx, tenant.ID)
	// Public services ingress: generated tag:ingress rules for live
	// services. Never persisted in HCL (spec §4.4).
	policyDoc = h.withIngressRules(ctx, tenant.ID, policyDoc)
```

Only Register computes `AllowedIps` (`allowedIPsFor` has one production caller, coordinator.go:708); heartbeat only compares revisions, so this is the single seam. Liveness changes bump the policy revision in Task 3, which makes peers re-register.

- [ ] **Step 8: Run tests**

Run: `go test -race ./internal/handlers/... ./internal/policy/... -count=1`
Expected: both packages `ok`.

- [ ] **Step 9: Commit**

```bash
git add apps/controller/internal/policy/generated.go apps/controller/internal/policy/generated_test.go apps/controller/internal/handlers/coordinator_ingress.go apps/controller/internal/handlers/coordinator_ingress_test.go apps/controller/internal/handlers/coordinator.go
git commit -m "feat(controller): generated tag:ingress ACL rules for live public services

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Ingress configuration and admin REST with audit

**Files:**
- Modify: `apps/controller/internal/config/config.go` (add `Ingress IngressConfig` to `Config`; env overrides in `applyEnvOverrides`)
- Test: `apps/controller/internal/config/config_test.go`
- Modify: `apps/controller/internal/server/http.go` (struct fields + `SetIngressConfig`)
- Modify: `apps/controller/internal/server/server.go` (after the `BAMBOO_RELAY_SIGNING_KEY` block, ~line 85)
- Create: `apps/controller/internal/server/api_ingress.go`
- Modify: `apps/controller/internal/server/api.go` (routeAPI dispatch switch, ~line 95)
- Modify: `apps/controller/internal/server/api_router.go` (`routePeersSub`, before the bare `"{id}"` branch)
- Test: `apps/controller/test/e2e/rest_ingress_test.go`
- Modify: `apps/controller/test/e2e/router_contract_test.go`, `apps/controller/test/e2e/web_route_contract_test.go`

**Interfaces:**
- Consumes: Task 1 repo API; `policy.IngressTag` (Task 2); `writePeerAuditTx`, `insertAuditTx`, `h.emitAuditHook`, `getPeerInTenant`, `h.requireAdmin`, `writeJSON`, `writeError`, `writeRouteMissing`, `h.coord.BumpPolicyRevision(ctx, tenantID) (int64, error)`; e2e helpers `startFixture`, `postJSON`, `jsonResponse`, `randomPubKey`, `f.mintJWT`, `f.enableRequireAuth`, `registerVictimPeerInSeparateTenant`, `probeBody`.
- Produces:
  - `config.IngressConfig{ SigningKey string; Suffix string }`, env `BAMBOO_INGRESS_SIGNING_KEY`, `BAMBOO_INGRESS_SUFFIX`
  - `func (h *HTTPServer) SetIngressConfig(key ed25519.PrivateKey, suffix string)`; fields `h.ingressSigningKey ed25519.PrivateKey`, `h.ingressSuffix string`
  - `func (h *HTTPServer) routeIngress(w, r, authn *authnContext, tenant *repo.Tenant) bool`
  - `func (h *HTTPServer) apiCreateIngress(w, r, authn, tenant, peerIDStr string)`
  - `type ingressServiceJSON` (camelCase response: `id, peerId, peerHostname, peerIp, label, url, targetPort, enabled, approved, approvedAt, live, aclRule, createdAt, updatedAt`)
  - `GET /api/v1/ingress` → `{"services": [...], "suffix": "<suffix>", "token": {"peerId","issuedAt","expiresAt"} | null}`
  - Request bodies follow the spec's field names: create `{label, target_port}`, patch `{enabled?, target_port?}`
  - `func ingressActorID(authn *authnContext) *uuid.UUID`, `func ingressApproverID(authn *authnContext) uuid.UUID`
  - e2e helpers: `type ingressTestPeer struct{ id, ip string }`, `registerIngressTestPeer(t, f, hostname) ingressTestPeer`, `tagPeer(t, f, peerID string, tags ...string)`, `ingressCall(t, method, url, bearer, tenantSlug string, payload any) jsonResponse`

- [ ] **Step 1: Write the failing config test**

Append to `apps/controller/internal/config/config_test.go`:

```go
func TestLoad_ingressEnv(t *testing.T) {
	t.Setenv("BAMBOO_INGRESS_SIGNING_KEY", "c2VlZA==")
	t.Setenv("BAMBOO_INGRESS_SUFFIX", "SVC.Example.Test.")
	cfg, err := Load(writeConfig(t, validBody))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ingress.SigningKey != "c2VlZA==" {
		t.Errorf("SigningKey = %q", cfg.Ingress.SigningKey)
	}
	if cfg.Ingress.Suffix != "svc.example.test" {
		t.Errorf("Suffix = %q, want lowercased without trailing dot", cfg.Ingress.Suffix)
	}
}
```

Run: `go test ./internal/config/ -run TestLoad_ingressEnv -count=1` → FAIL `cfg.Ingress undefined`.

- [ ] **Step 2: Implement config**

In `config.go`, add the field to `Config`:

```go
	Ingress     IngressConfig     `yaml:"ingress"`
```

and the type (next to `ReleaseFeedConfig`):

```go
// IngressConfig configures public services ingress
// (docs/design/2026-11-02-public-services-ingress.md). Both empty ⇒ the
// feature is off: admin REST still lists/creates rows, but ask answers
// 404 and token/routes endpoints answer 503.
type IngressConfig struct {
	// SigningKey is a base64 Ed25519 seed (same format as
	// relay_signing_key; generate with `controller relaykey` and use the
	// first value). It must differ from the relay signing key.
	SigningKey string `yaml:"signing_key"`
	// Suffix is the public DNS suffix, e.g. "svc.miilink.net".
	Suffix string `yaml:"suffix"`
}
```

In `applyEnvOverrides`, after the `BAMBOO_RELAY_SIGNING_KEY` block:

```go
	if v := os.Getenv("BAMBOO_INGRESS_SIGNING_KEY"); v != "" {
		c.Ingress.SigningKey = v
	}
	if v := os.Getenv("BAMBOO_INGRESS_SUFFIX"); v != "" {
		c.Ingress.Suffix = v
	}
	c.Ingress.Suffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Ingress.Suffix)), ".")
```

Run the config test again → PASS; `go test ./internal/config/ -count=1` → `ok`.

- [ ] **Step 3: Wire the HTTP server**

In `http.go` add to `HTTPServer` (after `relaySigningKey`):

```go
	// ingressSigningKey signs and verifies ingress service tokens
	// (public services ingress). Nil ⇒ token issue and the internal
	// routes/events endpoints answer 503. ingressSuffix is the public
	// DNS suffix ("svc.miilink.net"); empty ⇒ ask always answers 404.
	ingressSigningKey ed25519.PrivateKey
	ingressSuffix     string
```

and below `SetRelaySigningKey`:

```go
// SetIngressConfig wires public services ingress. key may be nil; suffix
// may be empty. Both are read per request, so tests may call this after
// NewHTTPServer.
func (h *HTTPServer) SetIngressConfig(key ed25519.PrivateKey, suffix string) {
	h.ingressSigningKey = key
	h.ingressSuffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(suffix)), ".")
}
```

In `server.go`, after the relay signing-key block:

```go
	// Public services ingress. A malformed key fails startup, like the
	// relay key; an empty one leaves the feature off.
	var ingressKey ed25519.PrivateKey
	if cfg.Ingress.SigningKey != "" {
		k, err := auth.ParseRelaySigningKey(cfg.Ingress.SigningKey)
		if err != nil {
			return nil, fmt.Errorf("parse BAMBOO_INGRESS_SIGNING_KEY: %w", err)
		}
		ingressKey = k
	}
	httpSrv.SetIngressConfig(ingressKey, cfg.Ingress.Suffix)
```

(add `"crypto/ed25519"` to `server.go` imports). `ParseRelaySigningKey` only decodes a 32-byte seed; reusing it keeps one key format.

Run: `go build ./... && go vet ./internal/server/` → no output.

- [ ] **Step 4: Write the failing REST tests**

`apps/controller/test/e2e/rest_ingress_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

type ingressTestPeer struct {
	id string
	ip string
}

// registerIngressTestPeer registers a peer through the dev-fallback path
// and force-approves it, so tests do not depend on the approval queue.
// Call it before f.enableRequireAuth().
func registerIngressTestPeer(t *testing.T, f *fixture, hostname string) ingressTestPeer {
	t.Helper()
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           hostname,
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if resp.status != http.StatusOK {
		t.Fatalf("register %s: status=%d body=%s", hostname, resp.status, resp.body)
	}
	var got struct {
		Self struct {
			ID string `json:"id"`
			IP string `json:"ip"`
		} `json:"self"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if _, err := f.admin.Exec(context.Background(),
		`UPDATE peers SET approval_status = 'approved', approved_at = now() WHERE id = $1`, got.Self.ID); err != nil {
		t.Fatalf("approve peer: %v", err)
	}
	return ingressTestPeer{id: got.Self.ID, ip: got.Self.IP}
}

func tagPeer(t *testing.T, f *fixture, peerID string, tags ...string) {
	t.Helper()
	if _, err := repo.NewPeers(f.admin).SetTags(context.Background(), uuid.MustParse(peerID), tags); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
}

// ingressCall sends a request with an optional bearer and, when tenantSlug
// is non-empty, the dev-fallback X-Tenant-Slug header.
func ingressCall(t *testing.T, method, url, bearer, tenantSlug string, payload any) jsonResponse {
	t.Helper()
	var body io.Reader
	if payload != nil {
		buf, _ := json.Marshal(payload)
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if tenantSlug != "" {
		req.Header.Set("X-Tenant-Slug", tenantSlug)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: out}
}

type ingressSvcShape struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	URL        string `json:"url"`
	TargetPort int    `json:"targetPort"`
	Enabled    bool   `json:"enabled"`
	Approved   bool   `json:"approved"`
	Live       bool   `json:"live"`
	ACLRule    string `json:"aclRule"`
}

func decodeIngressSvc(t *testing.T, r jsonResponse) ingressSvcShape {
	t.Helper()
	var s ingressSvcShape
	if err := json.Unmarshal(r.body, &s); err != nil {
		t.Fatalf("decode service: %v body=%s", err, r.body)
	}
	return s
}

func TestRESTIngress_Lifecycle(t *testing.T) {
	f := startFixture(t)
	f.httpAPI.SetIngressConfig(nil, "svc.example.test")
	mac := registerIngressTestPeer(t, f, "mac")
	f.enableRequireAuth()
	member := f.mintJWT(t, false)
	admin := f.mintJWT(t, true)
	label := "svc-" + uuid.NewString()[:8]
	base := f.httpURL + "/api/v1"

	created := ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", member, "",
		map[string]any{"label": label, "target_port": 47800})
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.body)
	}
	svc := decodeIngressSvc(t, created)
	if svc.Approved || svc.Enabled || svc.Live || svc.URL != "https://"+label+".svc.example.test" {
		t.Fatalf("new service = %+v", svc)
	}

	for name, body := range map[string]map[string]any{
		"duplicate": {"label": label, "target_port": 47800},
		"invalid":   {"label": "Bad_Label", "target_port": 47800},
		"reserved":  {"label": "www", "target_port": 47800},
		"low port":  {"label": "svc-" + uuid.NewString()[:8], "target_port": 80},
	} {
		want := http.StatusBadRequest
		if name == "duplicate" {
			want = http.StatusConflict
		}
		if r := ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", member, "", body); r.status != want {
			t.Errorf("%s: status=%d want %d body=%s", name, r.status, want, r.body)
		}
	}

	if r := ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", member, "", nil); r.status != http.StatusForbidden {
		t.Errorf("member approve: status=%d, want 403", r.status)
	}
	if r := ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, member, "", map[string]any{"enabled": true}); r.status != http.StatusForbidden {
		t.Errorf("member patch: status=%d, want 403", r.status)
	}
	appr := ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", admin, "", nil)
	if appr.status != http.StatusOK || !decodeIngressSvc(t, appr).Approved {
		t.Fatalf("admin approve: %d %s", appr.status, appr.body)
	}
	en := ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, admin, "", map[string]any{"enabled": true})
	enabled := decodeIngressSvc(t, en)
	if en.status != http.StatusOK || !enabled.Live || enabled.ACLRule != "tag:ingress -> "+mac.ip+":47800" {
		t.Fatalf("enable: %d %+v", en.status, enabled)
	}
	port := ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, admin, "", map[string]any{"target_port": 47801})
	moved := decodeIngressSvc(t, port)
	if port.status != http.StatusOK || moved.TargetPort != 47801 || moved.Approved || moved.Live {
		t.Fatalf("port change must reset approval: %d %+v", port.status, moved)
	}

	list := ingressCall(t, http.MethodGet, base+"/ingress", member, "", nil)
	if list.status != http.StatusOK || !strings.Contains(string(list.body), svc.ID) || !strings.Contains(string(list.body), `"suffix":"svc.example.test"`) {
		t.Fatalf("list: %d %s", list.status, list.body)
	}
	if r := ingressCall(t, http.MethodDelete, base+"/ingress/"+svc.ID, admin, "", nil); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := ingressCall(t, http.MethodDelete, base+"/ingress/"+svc.ID, admin, "", nil); r.status != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", r.status)
	}

	rows, err := f.admin.Query(context.Background(), `
		SELECT action FROM audit_log
		 WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1) AND action LIKE 'ingress.%'
		 ORDER BY occurred_at`, f.tenantSlug)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	want := "ingress.create,ingress.approve,ingress.enable,ingress.update,ingress.delete"
	if got := strings.Join(actions, ","); got != want {
		t.Errorf("audit actions = %s, want %s", got, want)
	}
}

func TestRESTIngress_ForeignPeerIsNotFound(t *testing.T) {
	f := startFixture(t)
	foreignPeer, _ := registerVictimPeerInSeparateTenant(t, f, nil)
	r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/peers/"+foreignPeer+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800})
	if r.status != http.StatusNotFound {
		t.Errorf("create on another tenant's peer: %d, want 404 (%s)", r.status, r.body)
	}
}

// TestRESTIngress_LivenessBumpsPolicyAndOpensACL checks the Task 2 seam end
// to end: a tenant with one authored rule denies ingress -> mac until the
// service is live, then the re-register response carries AllowedIPs both ways.
func TestRESTIngress_LivenessBumpsPolicyAndOpensACL(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	pol := ingressCall(t, http.MethodPut, f.httpURL+"/api/v1/policy", "", f.tenantSlug, map[string]any{
		"hclSource": "rule \"dev-to-db\" {\n  action = \"allow\"\n  sources = [\"tag:dev\"]\n  destinations = [\"tag:db:5432\"]\n}\n",
	})
	if pol.status != http.StatusOK {
		t.Fatalf("put policy: %d %s", pol.status, pol.body)
	}
	reRegister := func(peerID string) []byte {
		pub := getPeerPubKey(t, f, peerID)
		r := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
			"hostname": "x", "wireguardPublicKey": pub, "tenantSlug": f.tenantSlug,
		})
		if r.status != http.StatusOK {
			t.Fatalf("re-register: %d %s", r.status, r.body)
		}
		return r.body
	}
	if gwAllowedIPsContain(t, reRegister(vps.id), mac.id, mac.ip+"/32") {
		t.Fatal("ingress reaches mac before any service is live")
	}

	var revBefore int64
	_ = f.admin.QueryRow(context.Background(),
		`SELECT revision FROM acl_policies WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1)`, f.tenantSlug).Scan(&revBefore)

	base := f.httpURL + "/api/v1"
	svc := decodeIngressSvc(t, ingressCall(t, http.MethodPost, base+"/peers/"+mac.id+"/ingress", "", f.tenantSlug,
		map[string]any{"label": "svc-" + uuid.NewString()[:8], "target_port": 47800}))
	ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", "", f.tenantSlug, nil)
	ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, "", f.tenantSlug, map[string]any{"enabled": true})

	var revAfter int64
	_ = f.admin.QueryRow(context.Background(),
		`SELECT revision FROM acl_policies WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1)`, f.tenantSlug).Scan(&revAfter)
	if revAfter <= revBefore {
		t.Errorf("policy revision %d -> %d; enabling a live service must bump it", revBefore, revAfter)
	}
	if !gwAllowedIPsContain(t, reRegister(vps.id), mac.id, mac.ip+"/32") {
		t.Error("ingress peer's AllowedIPs for mac missing after the service went live")
	}
	if !gwAllowedIPsContain(t, reRegister(mac.id), vps.id, vps.ip+"/32") {
		t.Error("mac's AllowedIPs for the ingress peer missing (return path)")
	}

	ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, "", f.tenantSlug, map[string]any{"enabled": false})
	if gwAllowedIPsContain(t, reRegister(vps.id), mac.id, mac.ip+"/32") {
		t.Error("disabling the service must remove the generated rule")
	}
}
```

`getPeerPubKey` (peer_approval_test.go:373) and `gwAllowedIPsContain` (routes_exit_node_test.go:478) already exist in the package.

Add to `router_contract_test.go`, in `dispatched` before `// Policy + tooling.`:

```go
		// Public services ingress.
		{http.MethodGet, "/api/v1/ingress", false},
		{http.MethodPost, "/api/v1/peers/" + id + "/ingress", true},
		{http.MethodPost, "/api/v1/ingress/" + id + "/approve", false},
		{http.MethodPatch, "/api/v1/ingress/" + id, true},
		{http.MethodDelete, "/api/v1/ingress/" + id, false},
```

and to `wrongMethod`:

```go
		{http.MethodPut, "/api/v1/ingress", true},                         // GET-only
		{http.MethodGet, "/api/v1/ingress/" + id + "/approve", false},     // POST-only
		{http.MethodGet, "/api/v1/peers/" + id + "/ingress", false},       // POST-only
```

Add to `web_route_contract_test.go` `probes`, after the device-desk block:

```go
		// Public services ingress (apps/web ingress page).
		{method: http.MethodGet, path: "/api/v1/ingress"},
		{method: http.MethodPost, path: "/api/v1/peers/" + dummyID + "/ingress", noBody: true},
		{method: http.MethodPost, path: "/api/v1/ingress/" + dummyID + "/approve", noBody: true},
		{method: http.MethodPatch, path: "/api/v1/ingress/" + dummyID, noBody: true},
		{method: http.MethodDelete, path: "/api/v1/ingress/" + dummyID, noBody: true},
```

- [ ] **Step 5: Run to verify failure**

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/controller && go test ./test/e2e/ -run 'RESTIngress|RouterContract|WebRouteContract' -count=1`
Expected: FAIL — `TestRESTIngress_Lifecycle` create returns 404 (`route not registered` / unknown peer sub-path), contract subtests report the canonical no-route 404.

- [ ] **Step 6: Implement `api_ingress.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// Public services ingress admin REST
// (docs/design/2026-11-02-public-services-ingress.md §4.2). Create is open
// to any tenant member (it only files a pending request); approve, patch,
// delete, and token issue are admin-only.

const ingressReqMaxBytes = 1 << 16

type ingressServiceJSON struct {
	ID           string     `json:"id"`
	PeerID       string     `json:"peerId"`
	PeerHostname string     `json:"peerHostname"`
	PeerIP       string     `json:"peerIp"`
	Label        string     `json:"label"`
	URL          string     `json:"url"`
	TargetPort   int        `json:"targetPort"`
	Enabled      bool       `json:"enabled"`
	Approved     bool       `json:"approved"`
	ApprovedAt   *time.Time `json:"approvedAt,omitempty"`
	Live         bool       `json:"live"`
	ACLRule      string     `json:"aclRule,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type ingressTokenInfoJSON struct {
	PeerID    string    `json:"peerId"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ingressListJSON struct {
	Services []ingressServiceJSON `json:"services"`
	Suffix   string               `json:"suffix"`
	Token    *ingressTokenInfoJSON `json:"token"`
}

type apiCreateIngressReq struct {
	Label      string `json:"label"`
	TargetPort int    `json:"target_port"`
}

type apiPatchIngressReq struct {
	Enabled    *bool `json:"enabled"`
	TargetPort *int  `json:"target_port"`
}

// errIngressNotFound is the in-transaction sentinel for "missing or in
// another tenant"; both answer 404.
var errIngressNotFound = errors.New("ingress service not found")

func (h *HTTPServer) ingressServiceToJSON(s *repo.IngressService) ingressServiceJSON {
	out := ingressServiceJSON{
		ID: s.ID.String(), PeerID: s.PeerID.String(), PeerHostname: s.PeerHostname, PeerIP: s.PeerIP,
		Label: s.Label, TargetPort: s.TargetPort, Enabled: s.Enabled, Approved: s.Approved(),
		ApprovedAt: s.ApprovedAt, Live: s.Live(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	if h.ingressSuffix != "" {
		out.URL = "https://" + s.Label + "." + h.ingressSuffix
	}
	if out.Live {
		out.ACLRule = fmt.Sprintf("tag:%s -> %s:%d", policy.IngressTag, s.PeerIP, s.TargetPort)
	}
	return out
}

// ingressActorID is the user behind the request, when there is one.
func ingressActorID(authn *authnContext) *uuid.UUID {
	switch {
	case authn == nil:
		return nil
	case authn.claims != nil:
		id := authn.claims.UserID
		return &id
	case authn.apiToken != nil && authn.apiToken.CreatedBy != nil:
		id := *authn.apiToken.CreatedBy
		return &id
	}
	return nil
}

// ingressApproverID is what approved_by records; uuid.Nil when the
// approval came without a user identity (dev fallback).
func ingressApproverID(authn *authnContext) uuid.UUID {
	if id := ingressActorID(authn); id != nil {
		return *id
	}
	return uuid.Nil
}

// routeIngress handles /api/v1/ingress[/{sid}[/approve]].
func (h *HTTPServer) routeIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	if r.URL.Path == "/api/v1/ingress" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiListIngress(w, r, tenant)
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/ingress/")
	if !ok || rest == "" {
		return false
	}
	if sid, found := strings.CutSuffix(rest, "/approve"); found && sid != "" && !strings.Contains(sid, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiApproveIngress(w, r, authn, tenant, sid)
		return true
	}
	if !strings.Contains(rest, "/") {
		switch r.Method {
		case http.MethodPatch:
			h.apiPatchIngress(w, r, authn, tenant, rest)
		case http.MethodDelete:
			h.apiDeleteIngress(w, r, authn, tenant, rest)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	writeRouteMissing(w)
	return true
}

func (h *HTTPServer) apiListIngress(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	out := ingressListJSON{Services: []ingressServiceJSON{}, Suffix: h.ingressSuffix}
	err := db.WithTenant(r.Context(), h.pool, tenant.ID, func(q db.Querier) error {
		list, err := repo.NewIngressServices(q).ListByTenant(r.Context(), tenant.ID)
		if err != nil {
			return err
		}
		for _, s := range list {
			out.Services = append(out.Services, h.ingressServiceToJSON(s))
		}
		tok, err := repo.NewIngressTokens(q).Get(r.Context(), tenant.ID)
		if errors.Is(err, repo.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out.Token = &ingressTokenInfoJSON{PeerID: tok.PeerID.String(), IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// apiCreateIngress implements POST /api/v1/peers/{id}/ingress.
func (h *HTTPServer) apiCreateIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, peerIDStr string) {
	peerID, err := uuid.Parse(peerIDStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var req apiCreateIngressReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, ingressReqMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	req.Label = strings.ToLower(strings.TrimSpace(req.Label))
	if err := repo.ValidateIngressLabel(req.Label); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := repo.ValidateIngressPort(req.TargetPort); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	var (
		created *repo.IngressService
		auditEv *repo.AuditEvent
		auditOK bool
	)
	txErr := db.WithTenant(ctx, h.pool, tenant.ID, func(q db.Querier) error {
		peer, ok, gerr := getPeerInTenant(ctx, q, tenant.ID, peerID)
		if gerr != nil {
			return gerr
		}
		if !ok {
			return errIngressNotFound
		}
		svcs := repo.NewIngressServices(q)
		taken, terr := svcs.LabelTaken(ctx, req.Label)
		if terr != nil {
			return terr
		}
		if taken {
			return repo.ErrIngressLabelTaken
		}
		s, cerr := svcs.Create(ctx, tenant.ID, peerID, req.Label, req.TargetPort, ingressActorID(authn))
		if cerr != nil {
			return cerr
		}
		created = s
		auditEv, auditOK = writePeerAuditTx(ctx, q, authn, tenant.ID, peerID, "ingress.create", map[string]any{
			"service_id": s.ID, "label": s.Label, "target_port": s.TargetPort, "hostname": peer.Hostname,
		})
		return nil
	})
	switch {
	case errors.Is(txErr, errIngressNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(txErr, repo.ErrIngressLabelTaken):
		writeError(w, http.StatusConflict, txErr)
		return
	case txErr != nil:
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	h.emitAuditHook(ctx, auditEv, auditOK)
	writeJSON(w, http.StatusCreated, h.ingressServiceToJSON(created))
}

// loadIngressInTenant fetches a service inside the tenant tx; another
// tenant's row (or a missing one) is errIngressNotFound.
func loadIngressInTenant(ctx context.Context, svcs *repo.IngressServices, tenantID, id uuid.UUID) (*repo.IngressService, error) {
	s, err := svcs.GetByID(ctx, id)
	if errors.Is(err, repo.ErrNotFound) || (err == nil && s.TenantID != tenantID) {
		return nil, errIngressNotFound
	}
	return s, err
}

func (h *HTTPServer) apiApproveIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, sidStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.approve") {
		return
	}
	h.mutateIngress(w, r, tenant, sidStr, func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error) {
		if _, err := svcs.Approve(ctx, cur.ID, ingressApproverID(authn)); err != nil {
			return nil, nil, err
		}
		ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, "ingress.approve", map[string]any{
			"service_id": cur.ID, "label": cur.Label, "target_port": cur.TargetPort,
		})
		return []*repo.AuditEvent{ev}, []bool{ok}, nil
	})
}

func (h *HTTPServer) apiPatchIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, sidStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.update") {
		return
	}
	var req apiPatchIngressReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, ingressReqMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if req.Enabled == nil && req.TargetPort == nil {
		writeError(w, http.StatusBadRequest, errors.New("nothing to update: send enabled and/or target_port"))
		return
	}
	if req.TargetPort != nil {
		if err := repo.ValidateIngressPort(*req.TargetPort); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	h.mutateIngress(w, r, tenant, sidStr, func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error) {
		var evs []*repo.AuditEvent
		var oks []bool
		if req.TargetPort != nil && *req.TargetPort != cur.TargetPort {
			if _, err := svcs.SetTargetPort(ctx, cur.ID, *req.TargetPort); err != nil {
				return nil, nil, err
			}
			ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, "ingress.update", map[string]any{
				"service_id": cur.ID, "label": cur.Label,
				"target_port": map[string]any{"from": cur.TargetPort, "to": *req.TargetPort}, "approval_reset": true,
			})
			evs, oks = append(evs, ev), append(oks, ok)
		}
		if req.Enabled != nil && *req.Enabled != cur.Enabled {
			if _, err := svcs.SetEnabled(ctx, cur.ID, *req.Enabled); err != nil {
				return nil, nil, err
			}
			action := "ingress.disable"
			if *req.Enabled {
				action = "ingress.enable"
			}
			ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, action, map[string]any{
				"service_id": cur.ID, "label": cur.Label,
			})
			evs, oks = append(evs, ev), append(oks, ok)
		}
		return evs, oks, nil
	})
}

func (h *HTTPServer) apiDeleteIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, sidStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.delete") {
		return
	}
	h.mutateIngress(w, r, tenant, sidStr, func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error) {
		if _, err := svcs.Delete(ctx, cur.ID); err != nil {
			return nil, nil, err
		}
		ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, "ingress.delete", map[string]any{
			"service_id": cur.ID, "label": cur.Label, "target_port": cur.TargetPort,
		})
		return []*repo.AuditEvent{ev}, []bool{ok}, nil
	})
}

type ingressMutation func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error)

// mutateIngress is the shared shape of approve/patch/delete: load the
// row in the tenant tx, apply fn (which writes its own audit rows), read
// the row back, emit audit hooks after commit, bump the policy revision
// when liveness changed (Task 2's generated rules), and answer.
func (h *HTTPServer) mutateIngress(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant, sidStr string, fn ingressMutation) {
	sid, err := uuid.Parse(sidStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	var (
		before, after *repo.IngressService
		evs           []*repo.AuditEvent
		oks           []bool
	)
	txErr := db.WithTenant(ctx, h.pool, tenant.ID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		cur, lerr := loadIngressInTenant(ctx, svcs, tenant.ID, sid)
		if lerr != nil {
			return lerr
		}
		e, o, ferr := fn(ctx, q, svcs, cur)
		if ferr != nil {
			return ferr
		}
		before, evs, oks = cur, e, o
		upd, gerr := svcs.GetByID(ctx, sid)
		if errors.Is(gerr, repo.ErrNotFound) {
			return nil // deleted
		}
		after = upd
		return gerr
	})
	if errors.Is(txErr, errIngressNotFound) {
		http.NotFound(w, r)
		return
	}
	if txErr != nil {
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	for i := range evs {
		h.emitAuditHook(ctx, evs[i], oks[i])
	}
	wasLive := before.Live()
	isLive := after != nil && after.Live()
	if wasLive != isLive && h.coord != nil {
		if _, err := h.coord.BumpPolicyRevision(ctx, tenant.ID); err != nil {
			slog.Warn("ingress: bump policy revision", "tenant_id", tenant.ID, "err", err)
			writeError(w, http.StatusInternalServerError, fmt.Errorf("bump policy revision: %w", err))
			return
		}
	}
	if after == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, h.ingressServiceToJSON(after))
}
```

- [ ] **Step 7: Dispatch the routes**

In `api.go` `routeAPI`, add a case to the dispatch switch after `routeWebhooks`:

```go
	case h.routeWebhooks(w, r, authn, tenant):
	case h.routeIngress(w, r, authn, tenant):
	case h.routePolicy(w, r, authn, tenant):
```

In `api_router.go` `routePeersSub`, insert before the `// "{id}" — single peer GET/PATCH/DELETE.` block:

```go
	// "{id}/ingress" — file a public-service request for this peer
	// (docs/design/2026-11-02-public-services-ingress.md §4.2).
	if id, found := strings.CutSuffix(rest, "/ingress"); found && id != "" && !strings.Contains(id, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiCreateIngress(w, r, authn, tenant, id)
		return true
	}
```

- [ ] **Step 8: Run tests**

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/controller && go test -race ./test/e2e/ -run 'RESTIngress|RouterContract|WebRouteContract' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `--- PASS: TestRESTIngress_Lifecycle`, `--- PASS: TestRESTIngress_ForeignPeerIsNotFound`, `--- PASS: TestRESTIngress_LivenessBumpsPolicyAndOpensACL`, `--- PASS: TestRouterContract_AllRoutes`, `--- PASS: TestWebRouteContract`, then `ok`.

Then the full controller suite to prove nothing else moved: `go test -race ./... -count=1` → every package `ok` (or `?` for packages without tests).

- [ ] **Step 9: Commit**

```bash
git add apps/controller/internal/config apps/controller/internal/server/http.go apps/controller/internal/server/server.go apps/controller/internal/server/api.go apps/controller/internal/server/api_router.go apps/controller/internal/server/api_ingress.go apps/controller/test/e2e/rest_ingress_test.go apps/controller/test/e2e/router_contract_test.go apps/controller/test/e2e/web_route_contract_test.go
git commit -m "feat(controller): admin REST for public services ingress with audit

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Ingress service token, token issuance, and internal `ask` / `routes`

**Files:**
- Create: `apps/controller/internal/auth/ingress_token.go`
- Test: `apps/controller/internal/auth/ingress_token_test.go`
- Create: `apps/controller/internal/server/api_ingress_token.go`
- Create: `apps/controller/internal/server/api_ingress_internal.go`
- Test: `apps/controller/internal/server/api_ingress_internal_test.go` (package `server`, pure function)
- Modify: `apps/controller/internal/server/api_ingress.go` (`routeIngress`: add the `token` branch)
- Modify: `apps/controller/internal/server/http.go` (mount `/internal/v1/ingress/` in `NewHTTPServer`)
- Test: `apps/controller/test/e2e/internal_ingress_test.go`
- Modify: `apps/controller/test/e2e/router_contract_test.go`, `web_route_contract_test.go` (token route)

**Interfaces:**
- Consumes: Task 1 repo (`IngressTokens`, `ListLiveRoutes`, `LabelLive`); Task 3 `h.ingressSigningKey`, `h.ingressSuffix`, `routeIngress`, e2e helpers `registerIngressTestPeer`, `tagPeer`, `ingressCall`, `decodeIngressSvc`; `auth.ParseRelaySigningKey`, `auth.GenerateRelayKeypair`; `bearerToken(r)`.
- Produces:
  - `const auth.IngressRole = "ingress"`
  - `type auth.IngressClaims struct { TenantID uuid.UUID "tid"; PeerID uuid.UUID "pid"; Role string "role"; JTI uuid.UUID "jti"; IssuedAt int64 "iat"; ExpiresAt int64 "exp" }`
  - `func auth.IssueIngressToken(priv ed25519.PrivateKey, claims IngressClaims, ttl time.Duration) (string, error)`
  - `func auth.VerifyIngressToken(pub ed25519.PublicKey, token string) (*IngressClaims, error)`
  - `POST /api/v1/ingress/token` body `{peer_id}` → 201 `{"token","peerId","expiresAt"}` (admin; 503 when no signing key; 400 if the peer is not approved or lacks tag `ingress`)
  - `func (h *HTTPServer) routeInternalIngress(w, r)`; `func (h *HTTPServer) authenticateIngress(w, r) (*auth.IngressClaims, bool)`
  - `func ingressLabelFromDomain(domain, suffix string) (string, bool)`
  - `GET /internal/v1/ingress/ask?domain=` → 200 `ok` / 404; `GET /internal/v1/ingress/routes` → 200 JSON array `[{"service_id","label","peer_ip","target_port"}]` with `ETag`, 304 on `If-None-Match` match, 401 bad/revoked token, 503 when unconfigured
  - e2e helper `issueIngressToken(t, f, peerID string) string` (sets a fresh signing key on the fixture and returns a token)

- [ ] **Step 1: Write the failing token tests**

`apps/controller/internal/auth/ingress_token_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func ingressKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func validIngressClaims() auth.IngressClaims {
	return auth.IngressClaims{TenantID: uuid.New(), PeerID: uuid.New(), Role: auth.IngressRole, JTI: uuid.New()}
}

func TestIngressToken_RoundTrip(t *testing.T) {
	pub, priv := ingressKey(t)
	in := validIngressClaims()
	tok, err := auth.IssueIngressToken(priv, in, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := auth.VerifyIngressToken(pub, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TenantID != in.TenantID || got.PeerID != in.PeerID || got.JTI != in.JTI || got.Role != "ingress" || got.ExpiresAt <= time.Now().Unix() {
		t.Errorf("claims = %+v", got)
	}
}

func TestIngressToken_Rejects(t *testing.T) {
	pub, priv := ingressKey(t)
	otherPub, _ := ingressKey(t)
	good, _ := auth.IssueIngressToken(priv, validIngressClaims(), time.Hour)

	expiredClaims := validIngressClaims()
	expiredClaims.IssuedAt = time.Now().Add(-2 * time.Hour).Unix()
	expiredClaims.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	expired, _ := auth.IssueIngressToken(priv, expiredClaims, 0)

	// A relay token signed by the same key must not verify as an ingress
	// token (domain separation).
	relay, _ := auth.IssueRelayTokenEd25519(priv, auth.RelayClaims{TenantID: uuid.New(), PeerID: uuid.New()}, time.Hour)

	body, sig, _ := strings.Cut(good, ".")
	tampered := body + "x." + sig

	for name, tc := range map[string]struct {
		pub ed25519.PublicKey
		tok string
	}{
		"wrong key":   {otherPub, good},
		"expired":     {pub, expired},
		"relay token": {pub, relay},
		"tampered":    {pub, tampered},
		"garbage":     {pub, "not-a-token"},
	} {
		if _, err := auth.VerifyIngressToken(tc.pub, tc.tok); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: err=%v, want ErrInvalidToken", name, err)
		}
	}

	wrongRole := validIngressClaims()
	wrongRole.Role = "relay"
	if _, err := auth.IssueIngressToken(priv, wrongRole, time.Hour); err == nil {
		t.Error("issuing with a non-ingress role must fail")
	}
}
```

Run: `go test ./internal/auth/ -run IngressToken -count=1` → FAIL `undefined: auth.IngressClaims`.

- [ ] **Step 2: Implement `ingress_token.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// IngressRole is the only role an ingress service token carries.
const IngressRole = "ingress"

// ingressTokenDomain is mixed into the signed bytes so an ingress token
// never verifies as a relay or session token, even under a shared key.
const ingressTokenDomain = "bamboo.ingress-token.v1"

// IngressClaims authenticate the bamboo-ingress process to the
// controller's /internal/v1/ingress/* endpoints. PeerID is the ingress
// peer. JTI must equal the tenant's row in ingress_tokens; issuing a new
// token replaces it, which revokes the old one.
type IngressClaims struct {
	TenantID  uuid.UUID `json:"tid"`
	PeerID    uuid.UUID `json:"pid"`
	Role      string    `json:"role"`
	JTI       uuid.UUID `json:"jti"`
	IssuedAt  int64     `json:"iat"`
	ExpiresAt int64     `json:"exp"`
}

// IssueIngressToken signs claims with Ed25519. Wire format matches relay
// tokens: <base64url(json)>.<base64url(sig)>.
func IssueIngressToken(priv ed25519.PrivateKey, claims IngressClaims, ttl time.Duration) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("ingress signing key: want %d bytes, got %d", ed25519.PrivateKeySize, len(priv))
	}
	if claims.Role != IngressRole {
		return "", fmt.Errorf("ingress token role must be %q", IngressRole)
	}
	now := time.Now()
	if claims.IssuedAt == 0 {
		claims.IssuedAt = now.Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = now.Add(ttl).Unix()
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal ingress claims: %w", err)
	}
	encoded := base64Encode(body)
	sig := ed25519.Sign(priv, []byte(ingressTokenDomain+encoded))
	return encoded + "." + base64Encode(sig), nil
}

// VerifyIngressToken checks signature, role, ids, and expiry. Every
// failure is ErrInvalidToken. Revocation (jti) is the caller's check.
func VerifyIngressToken(pub ed25519.PublicKey, token string) (*IngressClaims, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrInvalidToken
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	rawSig, err := base64Decode(sig)
	if err != nil || !ed25519.Verify(pub, []byte(ingressTokenDomain+body), rawSig) {
		return nil, ErrInvalidToken
	}
	rawBody, err := base64Decode(body)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var claims IngressClaims
	if err := json.Unmarshal(rawBody, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if claims.Role != IngressRole || claims.TenantID == uuid.Nil || claims.PeerID == uuid.Nil || claims.JTI == uuid.Nil {
		return nil, ErrInvalidToken
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}
```

Run: `go test -race ./internal/auth/ -count=1` → `ok`.

- [ ] **Step 3: Write the failing domain-parsing unit test**

`apps/controller/internal/server/api_ingress_internal_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "testing"

func TestIngressLabelFromDomain(t *testing.T) {
	const suffix = "svc.miilink.net"
	cases := []struct {
		domain, suffix, want string
		ok                   bool
	}{
		{"yeschef.svc.miilink.net", suffix, "yeschef", true},
		{"YesChef.SVC.miilink.net.", suffix, "yeschef", true},
		{"a.b.svc.miilink.net", suffix, "", false},
		{"svc.miilink.net", suffix, "", false},
		{"yeschef.miilink.net", suffix, "", false},
		{"www.svc.miilink.net", suffix, "", false},
		{"bad_label.svc.miilink.net", suffix, "", false},
		{"yeschef.svc.miilink.net", "", "", false},
		{"", suffix, "", false},
	}
	for _, c := range cases {
		got, ok := ingressLabelFromDomain(c.domain, c.suffix)
		if got != c.want || ok != c.ok {
			t.Errorf("ingressLabelFromDomain(%q, %q) = %q, %v; want %q, %v", c.domain, c.suffix, got, ok, c.want, c.ok)
		}
	}
}
```

Run: `go test ./internal/server/ -run TestIngressLabelFromDomain -count=1` → FAIL `undefined: ingressLabelFromDomain`.

- [ ] **Step 4: Write the failing internal-endpoint tests**

`apps/controller/test/e2e/internal_ingress_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

// issueIngressToken gives the fixture a fresh signing key and issues a
// token for peerID through the admin REST endpoint (dev-fallback admin).
func issueIngressToken(t *testing.T, f *fixture, peerID string) string {
	t.Helper()
	seed, _, err := auth.GenerateRelayKeypair()
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.ParseRelaySigningKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	f.httpAPI.SetIngressConfig(key, "svc.example.test")
	r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": peerID})
	if r.status != http.StatusCreated {
		t.Fatalf("issue token: %d %s", r.status, r.body)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.body, &out)
	return out.Token
}

func internalGet(t *testing.T, url, token, ifNoneMatch string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// makeLiveService creates, approves, and enables a service via REST.
func makeLiveService(t *testing.T, f *fixture, peerID string) (id, label string) {
	t.Helper()
	label = "svc-" + uuid.NewString()[:8]
	base := f.httpURL + "/api/v1"
	svc := decodeIngressSvc(t, ingressCall(t, http.MethodPost, base+"/peers/"+peerID+"/ingress", "", f.tenantSlug,
		map[string]any{"label": label, "target_port": 47800}))
	ingressCall(t, http.MethodPost, base+"/ingress/"+svc.ID+"/approve", "", f.tenantSlug, nil)
	ingressCall(t, http.MethodPatch, base+"/ingress/"+svc.ID, "", f.tenantSlug, map[string]any{"enabled": true})
	return svc.ID, label
}

func TestInternalIngress_TokenIssueRequiresIngressPeer(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	seed, _, _ := auth.GenerateRelayKeypair()
	key, _ := auth.ParseRelaySigningKey(seed)

	// No key configured ⇒ 503.
	if r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": mac.id}); r.status != http.StatusServiceUnavailable {
		t.Errorf("unconfigured: %d, want 503", r.status)
	}
	f.httpAPI.SetIngressConfig(key, "svc.example.test")
	// Untagged peer ⇒ 400.
	if r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": mac.id}); r.status != http.StatusBadRequest {
		t.Errorf("untagged peer: %d, want 400 (%s)", r.status, r.body)
	}
}

func TestInternalIngress_AskAndRoutes(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	token := issueIngressToken(t, f, vps.id)
	svcID, label := makeLiveService(t, f, mac.id)

	ask := f.httpURL + "/internal/v1/ingress/ask?domain="
	if resp, _ := internalGet(t, ask+label+".svc.example.test", "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("ask live: %d, want 200", resp.StatusCode)
	}
	for _, d := range []string{"nope-" + uuid.NewString()[:6] + ".svc.example.test", label + ".other.test", ""} {
		if resp, _ := internalGet(t, ask+d, "", ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("ask %q: %d, want 404", d, resp.StatusCode)
		}
	}

	routesURL := f.httpURL + "/internal/v1/ingress/routes"
	if resp, _ := internalGet(t, routesURL, "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("routes without token: %d, want 401", resp.StatusCode)
	}
	resp, body := internalGet(t, routesURL, token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("routes: %d %s", resp.StatusCode, body)
	}
	var routes []struct {
		ServiceID  string `json:"service_id"`
		Label      string `json:"label"`
		PeerIP     string `json:"peer_ip"`
		TargetPort int    `json:"target_port"`
	}
	if err := json.Unmarshal(body, &routes); err != nil {
		t.Fatalf("decode routes: %v %s", err, body)
	}
	if len(routes) != 1 || routes[0].ServiceID != svcID || routes[0].Label != label || routes[0].PeerIP != mac.ip || routes[0].TargetPort != 47800 {
		t.Fatalf("routes = %+v", routes)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("routes response has no ETag")
	}
	if resp2, _ := internalGet(t, routesURL, token, etag); resp2.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match: %d, want 304", resp2.StatusCode)
	}

	// Disable ⇒ ask 404, ETag changes, routes empty.
	ingressCall(t, http.MethodPatch, f.httpURL+"/api/v1/ingress/"+svcID, "", f.tenantSlug, map[string]any{"enabled": false})
	if resp, _ := internalGet(t, ask+label+".svc.example.test", "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("ask after disable: %d, want 404", resp.StatusCode)
	}
	resp3, body3 := internalGet(t, routesURL, token, etag)
	if resp3.StatusCode != http.StatusOK || string(body3) != "[]" {
		t.Errorf("routes after disable: %d %s, want 200 []", resp3.StatusCode, body3)
	}

	// Rotation revokes the old token.
	newToken := issueIngressTokenSameKey(t, f, vps.id)
	if resp, _ := internalGet(t, routesURL, token, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("rotated-out token: %d, want 401", resp.StatusCode)
	}
	if resp, _ := internalGet(t, routesURL, newToken, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("new token: %d, want 200", resp.StatusCode)
	}
}

// TestInternalIngress_RoutesAreTenantScoped: a token only sees its tenant.
func TestInternalIngress_RoutesAreTenantScoped(t *testing.T) {
	a := startFixture(t)
	macA := registerIngressTestPeer(t, a, "mac-a")
	makeLiveService(t, a, macA.id)

	b := startFixture(t)
	vpsB := registerIngressTestPeer(t, b, "vps-b")
	tagPeer(t, b, vpsB.id, "ingress")
	tokenB := issueIngressToken(t, b, vpsB.id)
	resp, body := internalGet(t, b.httpURL+"/internal/v1/ingress/routes", tokenB, "")
	if resp.StatusCode != http.StatusOK || string(body) != "[]" {
		t.Errorf("tenant B routes = %d %s, want 200 []", resp.StatusCode, body)
	}
}

// issueIngressTokenSameKey re-issues with the key already on the fixture.
func issueIngressTokenSameKey(t *testing.T, f *fixture, peerID string) string {
	t.Helper()
	r := ingressCall(t, http.MethodPost, f.httpURL+"/api/v1/ingress/token", "", f.tenantSlug, map[string]any{"peer_id": peerID})
	if r.status != http.StatusCreated {
		t.Fatalf("re-issue: %d %s", r.status, r.body)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.body, &out)
	return out.Token
}
```

Note: both fixtures in `RoutesAreTenantScoped` share one database and use distinct tenant slugs; the `ask` endpoint is global by design, `routes` is not.

Add to `router_contract_test.go` `dispatched`: `{http.MethodPost, "/api/v1/ingress/token", true},` and to `wrongMethod`: `{http.MethodGet, "/api/v1/ingress/token", false}, // POST-only`. Add to `web_route_contract_test.go` probes: `{method: http.MethodPost, path: "/api/v1/ingress/token", noBody: true},`.

Run: `go test ./test/e2e/ -run 'InternalIngress|RouterContract|WebRouteContract' -count=1`
Expected: FAIL — token issue returns 404 (`/api/v1/ingress/token` parses `token` as a service id) and `/internal/...` returns the mux 404.

- [ ] **Step 5: Implement token issuance (`api_ingress_token.go`)**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// ingressTokenTTL bounds a token stored on the VPS. Rotation (issuing a
// new one) revokes the old one immediately via ingress_tokens.jti.
const ingressTokenTTL = 365 * 24 * time.Hour

var errIngressPeerNotEligible = errors.New("ingress peer must be approved and carry tag:ingress")

type apiIssueIngressTokenReq struct {
	PeerID string `json:"peer_id"`
}

type apiIssueIngressTokenResp struct {
	Token     string    `json:"token"`
	PeerID    string    `json:"peerId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// apiIssueIngressToken implements POST /api/v1/ingress/token. The token
// is returned once and never stored; only its jti is kept.
func (h *HTTPServer) apiIssueIngressToken(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.token.issue") {
		return
	}
	if len(h.ingressSigningKey) == 0 {
		writeError(w, http.StatusServiceUnavailable, errors.New("public services ingress is not configured (BAMBOO_INGRESS_SIGNING_KEY)"))
		return
	}
	var req apiIssueIngressTokenReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, ingressReqMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	peerID, err := uuid.Parse(req.PeerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("peer_id must be a UUID"))
		return
	}
	now := time.Now()
	claims := auth.IngressClaims{
		TenantID: tenant.ID, PeerID: peerID, Role: auth.IngressRole, JTI: uuid.New(),
		IssuedAt: now.Unix(), ExpiresAt: now.Add(ingressTokenTTL).Unix(),
	}
	token, err := auth.IssueIngressToken(h.ingressSigningKey, claims, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ctx := r.Context()
	expires := time.Unix(claims.ExpiresAt, 0).UTC()
	var (
		auditEv *repo.AuditEvent
		auditOK bool
	)
	txErr := db.WithTenant(ctx, h.pool, tenant.ID, func(q db.Querier) error {
		peer, ok, gerr := getPeerInTenant(ctx, q, tenant.ID, peerID)
		if gerr != nil {
			return gerr
		}
		if !ok {
			return errIngressNotFound
		}
		if peer.ApprovalStatus != "approved" || !slices.Contains(peer.Tags, policy.IngressTag) {
			return errIngressPeerNotEligible
		}
		if err := repo.NewIngressTokens(q).Put(ctx, repo.IngressToken{
			TenantID: tenant.ID, PeerID: peerID, JTI: claims.JTI, IssuedBy: ingressActorID(authn), ExpiresAt: expires,
		}); err != nil {
			return err
		}
		// Never put the token itself in the audit diff.
		auditEv, auditOK = writePeerAuditTx(ctx, q, authn, tenant.ID, peerID, "ingress.token.issue", map[string]any{
			"jti": claims.JTI, "expires_at": expires,
		})
		return nil
	})
	switch {
	case errors.Is(txErr, errIngressNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(txErr, errIngressPeerNotEligible):
		writeError(w, http.StatusBadRequest, txErr)
		return
	case txErr != nil:
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	h.emitAuditHook(ctx, auditEv, auditOK)
	writeJSON(w, http.StatusCreated, apiIssueIngressTokenResp{Token: token, PeerID: peerID.String(), ExpiresAt: expires})
}
```

In `api_ingress.go` `routeIngress`, insert right after the `rest, ok := …` check:

```go
	if rest == "token" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiIssueIngressToken(w, r, authn, tenant)
		return true
	}
```

- [ ] **Step 6: Implement the internal endpoints (`api_ingress_internal.go`)**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// /internal/v1/ingress/* is the ingress ↔ controller surface
// (docs/design/2026-11-02-public-services-ingress.md §4.3). It is never
// routed by the public Caddy sites: the control-plane site sends only
// /api/*, /auth/*, /healthz, /install.sh to the controller, and both
// public sites answer /internal/* with 404.

type ingressRouteJSON struct {
	ServiceID  string `json:"service_id"`
	Label      string `json:"label"`
	PeerIP     string `json:"peer_ip"`
	TargetPort int    `json:"target_port"`
}

func (h *HTTPServer) routeInternalIngress(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/internal/v1/ingress/ask":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.internalIngressAsk(w, r)
	case "/internal/v1/ingress/routes":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.internalIngressRoutes(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ingressLabelFromDomain extracts <label> from "<label>.<suffix>".
// Exactly one DNS label is allowed in front of the suffix.
func ingressLabelFromDomain(domain, suffix string) (string, bool) {
	if suffix == "" {
		return "", false
	}
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	label, ok := strings.CutSuffix(d, "."+suffix)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	if repo.ValidateIngressLabel(label) != nil {
		return "", false
	}
	return label, true
}

// internalIngressAsk answers Caddy's on-demand TLS permission check. No
// token (Caddy's ask cannot send headers); it reveals one bit per name.
func (h *HTTPServer) internalIngressAsk(w http.ResponseWriter, r *http.Request) {
	label, ok := ingressLabelFromDomain(r.URL.Query().Get("domain"), h.ingressSuffix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// RLS bootstrap: SECURITY DEFINER function, no tenant GUC needed.
	live, err := repo.NewIngressServices(h.pool).LabelLive(r.Context(), label)
	if err != nil {
		slog.Warn("ingress ask lookup", "label", label, "err", err)
		http.Error(w, "lookup failed", http.StatusServiceUnavailable)
		return
	}
	if !live {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

// authenticateIngress verifies the bearer ingress token and that its jti
// is the tenant's current one. Writes the error response itself.
func (h *HTTPServer) authenticateIngress(w http.ResponseWriter, r *http.Request) (*auth.IngressClaims, bool) {
	if len(h.ingressSigningKey) == 0 {
		writeError(w, http.StatusServiceUnavailable, errors.New("public services ingress is not configured"))
		return nil, false
	}
	tok := bearerToken(r)
	if tok == "" {
		writeError(w, http.StatusUnauthorized, errors.New("ingress token required"))
		return nil, false
	}
	pub, _ := h.ingressSigningKey.Public().(ed25519.PublicKey)
	claims, err := auth.VerifyIngressToken(pub, tok)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid ingress token"))
		return nil, false
	}
	var current *repo.IngressToken
	err = db.WithTenant(r.Context(), h.pool, claims.TenantID, func(q db.Querier) error {
		t, gerr := repo.NewIngressTokens(q).Get(r.Context(), claims.TenantID)
		current = t
		return gerr
	})
	if errors.Is(err, repo.ErrNotFound) || (err == nil && (current.JTI != claims.JTI || current.PeerID != claims.PeerID)) {
		writeError(w, http.StatusUnauthorized, errors.New("ingress token revoked"))
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return nil, false
	}
	return claims, true
}

// internalIngressRoutes returns the token tenant's live routes. The ETag
// is a hash of the body, so an unchanged table costs a 304.
func (h *HTTPServer) internalIngressRoutes(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateIngress(w, r)
	if !ok {
		return
	}
	var routes []repo.IngressRoute
	err := db.WithTenant(r.Context(), h.pool, claims.TenantID, func(q db.Querier) error {
		rt, lerr := repo.NewIngressServices(q).ListLiveRoutes(r.Context(), claims.TenantID)
		routes = rt
		return lerr
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]ingressRouteJSON, 0, len(routes))
	for _, rt := range routes {
		out = append(out, ingressRouteJSON{ServiceID: rt.ServiceID.String(), Label: rt.Label, PeerIP: rt.PeerIP, TargetPort: rt.TargetPort})
	}
	body, err := json.Marshal(out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
```

In `http.go` `NewHTTPServer`, after `mux.HandleFunc("/api/v1/", h.routeAPI)`:

```go
	// Public services ingress ↔ controller. Reached only from inside the
	// compose network / host loopback; never routed by the public Caddy
	// sites (spec §4.3).
	mux.HandleFunc("/internal/v1/ingress/", h.routeInternalIngress)
```

- [ ] **Step 7: Run tests**

Run:
```bash
cd /Users/hanfourhuang/Projects/bamboo/apps/controller
go test -race ./internal/auth/ ./internal/server/ -count=1
go test -race ./test/e2e/ -run 'InternalIngress|RESTIngress|RouterContract|WebRouteContract' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
Expected: `ok` for both packages; every listed e2e test `--- PASS`, then `ok`.

- [ ] **Step 8: Commit**

```bash
git add apps/controller/internal/auth/ingress_token.go apps/controller/internal/auth/ingress_token_test.go apps/controller/internal/server/api_ingress_token.go apps/controller/internal/server/api_ingress_internal.go apps/controller/internal/server/api_ingress_internal_test.go apps/controller/internal/server/api_ingress.go apps/controller/internal/server/http.go apps/controller/test/e2e/internal_ingress_test.go apps/controller/test/e2e/router_contract_test.go apps/controller/test/e2e/web_route_contract_test.go
git commit -m "feat(controller): ingress service token and internal ask/routes endpoints

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Telemetry ingest into `connection_events`

**Files:**
- Modify: `apps/controller/internal/clickhouse/clickhouse.go` (`applySchema` statement list; additive `ALTER TABLE … ADD COLUMN IF NOT EXISTS`)
- Create: `apps/controller/internal/clickhouse/connections_ingress.go`
- Test: `apps/controller/internal/clickhouse/connections_ingress_test.go`
- Modify: `apps/controller/internal/server/api_ingress_internal.go` (add `events` case + handler)
- Test: `apps/controller/test/e2e/internal_ingress_test.go` (append)

**Interfaces:**
- Consumes: `h.connEvents *clickhouse.ConnectionEvents` (http.go), `authenticateIngress` (Task 4), `repo.IngressServices.ListByTenant` (Task 1), `requireCH(t)` (clickhouse_test.go), `CountByTenant(ctx, tenantID, since)` (existing reader used in connections_test.go).
- Produces:
  - `const clickhouse.IngressRequestEventType = "ingress_request"`
  - `type clickhouse.IngressRequestEvent struct { TenantID, IngressPeerID, TargetPeerID, ServiceID uuid.UUID; ClientIP string; Status uint16; BytesIn, BytesOut uint64; DurationMs uint32; OccurredAt time.Time }`
  - `func (r *ConnectionEvents) InsertIngressBatch(ctx context.Context, events []IngressRequestEvent) error`
  - `POST /internal/v1/ingress/events` body `{"events":[{"service_id","client_ip","status","bytes_in","bytes_out","duration_ms","occurred_at"}]}` → 202 `{"accepted": n}`; max 1000 events, 1 MiB body. Rows whose `service_id` is not in the token tenant are dropped.

Column mapping (documented in the code): `event_type='ingress_request'`, `path='ingress'`, `source_peer_id` = ingress peer (from the token), `destination_peer_id` = target peer, `bytes_sent` = bytes_out (to client), `bytes_received` = bytes_in (from client), plus new columns `service_id`, `client_ip`, `http_status`, `duration_ms`. No bodies, no headers.

- [ ] **Step 1: Write the failing ClickHouse test**

`apps/controller/internal/clickhouse/connections_ingress_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"
)

func TestConnectionEvents_InsertIngressBatch(t *testing.T) {
	c := requireCH(t)
	ctx := context.Background()
	events := clickhouse.NewConnectionEvents(c)
	tenantID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	batch := []clickhouse.IngressRequestEvent{
		{TenantID: tenantID, IngressPeerID: uuid.New(), TargetPeerID: uuid.New(), ServiceID: uuid.New(),
			ClientIP: "203.0.113.7", Status: 200, BytesIn: 120, BytesOut: 4096, DurationMs: 35, OccurredAt: now},
		{TenantID: tenantID, IngressPeerID: uuid.New(), TargetPeerID: uuid.New(), ServiceID: uuid.New(),
			ClientIP: "203.0.113.8", Status: 429, OccurredAt: now.Add(time.Second)},
	}
	if err := events.InsertIngressBatch(ctx, batch); err != nil {
		t.Fatalf("InsertIngressBatch: %v", err)
	}
	n, err := events.CountByTenant(ctx, tenantID, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("CountByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

func TestConnectionEvents_InsertIngressBatchNilConn(t *testing.T) {
	events := clickhouse.NewConnectionEvents(nil)
	if err := events.InsertIngressBatch(context.Background(), []clickhouse.IngressRequestEvent{{TenantID: uuid.New()}}); err != nil {
		t.Errorf("nil conn must degrade silently, got %v", err)
	}
}
```

Run: `go test ./internal/clickhouse/ -run Ingress -count=1` → FAIL `undefined: clickhouse.IngressRequestEvent`.

- [ ] **Step 2: Add the columns and the writer**

In `clickhouse.go` `applySchema`, append to `stmts` right after the existing `ADD COLUMN IF NOT EXISTS prev_path` statement:

```go
		// Public services ingress request rows (event_type
		// 'ingress_request'). Additive; existing writers leave the
		// defaults ('' / 0).
		`ALTER TABLE connection_events ADD COLUMN IF NOT EXISTS service_id String`,
		`ALTER TABLE connection_events ADD COLUMN IF NOT EXISTS client_ip String`,
		`ALTER TABLE connection_events ADD COLUMN IF NOT EXISTS http_status UInt16`,
		`ALTER TABLE connection_events ADD COLUMN IF NOT EXISTS duration_ms UInt32`,
```

`apps/controller/internal/clickhouse/connections_ingress.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// IngressRequestEventType marks one proxied public-services request.
const IngressRequestEventType = "ingress_request"

// IngressRequestEvent is one request through bamboo-ingress. It carries
// metadata only: never request or response bodies, never headers.
//
// Mapping onto connection_events: source_peer_id = ingress peer,
// destination_peer_id = target peer, bytes_sent = bytes to the client,
// bytes_received = bytes from the client, path = "ingress".
type IngressRequestEvent struct {
	TenantID      uuid.UUID
	IngressPeerID uuid.UUID
	TargetPeerID  uuid.UUID
	ServiceID     uuid.UUID
	ClientIP      string
	Status        uint16
	BytesIn       uint64
	BytesOut      uint64
	DurationMs    uint32
	OccurredAt    time.Time
}

// InsertIngressBatch writes events in one round-trip. No-op when
// ClickHouse is not configured or the batch is empty.
func (r *ConnectionEvents) InsertIngressBatch(ctx context.Context, events []IngressRequestEvent) error {
	if !r.c.IsConfigured() {
		r.skip.Do(func() { r.logs.do("connection_events.insert_ingress") })
		return nil
	}
	if len(events) == 0 {
		return nil
	}
	batch, err := r.c.driver().PrepareBatch(ctx, `
		INSERT INTO connection_events (
		    id, tenant_id, occurred_at, source_peer_id, destination_peer_id,
		    event_type, path, bytes_sent, bytes_received,
		    service_id, client_ip, http_status, duration_ms
		)`)
	if err != nil {
		return fmt.Errorf("prepare ingress batch: %w", err)
	}
	now := time.Now().UTC()
	for _, e := range events {
		at := e.OccurredAt
		if at.IsZero() {
			at = now
		}
		if err := batch.Append(
			uuid.New(), e.TenantID, at, e.IngressPeerID, e.TargetPeerID,
			IngressRequestEventType, "ingress", e.BytesOut, e.BytesIn,
			e.ServiceID.String(), e.ClientIP, e.Status, e.DurationMs,
		); err != nil {
			return fmt.Errorf("append ingress event: %w", err)
		}
	}
	return batch.Send()
}
```

Run (with `CLICKHOUSE_URL_TEST` set): `go test -race ./internal/clickhouse/ -count=1` → `ok` (without it, the CH test SKIPs and the nil-conn test passes).

- [ ] **Step 3: Write the failing events-endpoint test**

Append to `apps/controller/test/e2e/internal_ingress_test.go` (add `"bytes"` and `"time"` to its imports):

```go
func postIngressEvents(t *testing.T, url, token string, payload any) jsonResponse {
	t.Helper()
	buf, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST events: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: body}
}

func TestInternalIngress_EventsAcceptsOnlyOwnServices(t *testing.T) {
	f := startFixture(t)
	mac := registerIngressTestPeer(t, f, "mac")
	vps := registerIngressTestPeer(t, f, "vps")
	tagPeer(t, f, vps.id, "ingress")
	token := issueIngressToken(t, f, vps.id)
	svcID, _ := makeLiveService(t, f, mac.id)
	url := f.httpURL + "/internal/v1/ingress/events"
	ev := func(sid string) map[string]any {
		return map[string]any{"service_id": sid, "client_ip": "203.0.113.7", "status": 200,
			"bytes_in": 10, "bytes_out": 20, "duration_ms": 5, "occurred_at": time.Now().UTC()}
	}

	if r := postIngressEvents(t, url, "", map[string]any{"events": []any{ev(svcID)}}); r.status != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", r.status)
	}
	r := postIngressEvents(t, url, token, map[string]any{"events": []any{ev(svcID), ev(uuid.NewString()), ev("not-a-uuid")}})
	if r.status != http.StatusAccepted || string(bytes.TrimSpace(r.body)) != `{"accepted":1}` {
		t.Errorf("events: %d %s, want 202 {\"accepted\":1}", r.status, r.body)
	}
	tooMany := make([]any, 1001)
	for i := range tooMany {
		tooMany[i] = ev(svcID)
	}
	if r := postIngressEvents(t, url, token, map[string]any{"events": tooMany}); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("1001 events: %d, want 413", r.status)
	}
}
```

The fixture has no ClickHouse, so the writer degrades to a no-op; acceptance counting is what this test pins. The CH write itself is covered by Step 1.

Run: `go test ./test/e2e/ -run TestInternalIngress_Events -count=1` → FAIL (mux `404 page not found` for `/internal/v1/ingress/events`).

- [ ] **Step 4: Implement the handler**

In `api_ingress_internal.go`, add to the `routeInternalIngress` switch:

```go
	case "/internal/v1/ingress/events":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.internalIngressEvents(w, r)
```

and the handler (add imports `"net/netip"`, `"time"`, `"github.com/google/uuid"`, `"github.com/hanfour/bamboo/apps/controller/internal/clickhouse"`):

```go
const (
	maxIngressEventsPerBatch = 1000
	maxIngressEventsBody     = 1 << 20
)

type ingressEventJSON struct {
	ServiceID  string    `json:"service_id"`
	ClientIP   string    `json:"client_ip"`
	Status     int       `json:"status"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	DurationMs int64     `json:"duration_ms"`
	OccurredAt time.Time `json:"occurred_at"`
}

type ingressEventsReq struct {
	Events []ingressEventJSON `json:"events"`
}

// internalIngressEvents stores per-request metadata from bamboo-ingress
// in ClickHouse connection_events. Rows naming a service outside the
// token's tenant, or with out-of-range values, are dropped.
func (h *HTTPServer) internalIngressEvents(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateIngress(w, r)
	if !ok {
		return
	}
	var req ingressEventsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngressEventsBody)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("events body too large"))
			return
		}
		writeError(w, http.StatusBadRequest, errors.New("decode events"))
		return
	}
	if len(req.Events) > maxIngressEventsPerBatch {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("too many events in one batch"))
		return
	}
	peerByService := map[uuid.UUID]uuid.UUID{}
	err := db.WithTenant(r.Context(), h.pool, claims.TenantID, func(q db.Querier) error {
		list, lerr := repo.NewIngressServices(q).ListByTenant(r.Context(), claims.TenantID)
		for _, s := range list {
			peerByService[s.ID] = s.PeerID
		}
		return lerr
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	rows := make([]clickhouse.IngressRequestEvent, 0, len(req.Events))
	for _, e := range req.Events {
		row, ok := ingressEventRow(e, claims, peerByService)
		if ok {
			rows = append(rows, row)
		}
	}
	if err := h.connEvents.InsertIngressBatch(r.Context(), rows); err != nil {
		slog.Warn("ingress events insert", "tenant_id", claims.TenantID, "n", len(rows), "err", err)
		writeError(w, http.StatusBadGateway, errors.New("telemetry store unavailable"))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(rows)})
}

func ingressEventRow(e ingressEventJSON, claims *auth.IngressClaims, peerByService map[uuid.UUID]uuid.UUID) (clickhouse.IngressRequestEvent, bool) {
	sid, err := uuid.Parse(e.ServiceID)
	if err != nil {
		return clickhouse.IngressRequestEvent{}, false
	}
	target, ok := peerByService[sid]
	if !ok || e.Status < 100 || e.Status > 599 || e.BytesIn < 0 || e.BytesOut < 0 || e.DurationMs < 0 {
		return clickhouse.IngressRequestEvent{}, false
	}
	clientIP := ""
	if addr, perr := netip.ParseAddr(e.ClientIP); perr == nil {
		clientIP = addr.String()
	}
	dur := e.DurationMs
	if dur > int64(^uint32(0)) {
		dur = int64(^uint32(0))
	}
	return clickhouse.IngressRequestEvent{
		TenantID: claims.TenantID, IngressPeerID: claims.PeerID, TargetPeerID: target, ServiceID: sid,
		ClientIP: clientIP, Status: uint16(e.Status), BytesIn: uint64(e.BytesIn), BytesOut: uint64(e.BytesOut),
		DurationMs: uint32(dur), OccurredAt: e.OccurredAt,
	}, true
}
```

`ingressEventRow` keeps `internalIngressEvents` under 50 lines and nesting shallow.

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/clickhouse/ ./internal/server/ ./test/e2e/ -run 'Ingress' -count=1` → three `ok` lines. Then `go test -race ./... -count=1` → all `ok`.

- [ ] **Step 6: Commit**

```bash
git add apps/controller/internal/clickhouse apps/controller/internal/server/api_ingress_internal.go apps/controller/test/e2e/internal_ingress_test.go
git commit -m "feat(controller): ingest public-services request telemetry into connection_events

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: `apps/ingress` module, rate limiter, routes table and poller

**Files:**
- Create: `apps/ingress/go.mod`
- Modify: `go.work` (add `./apps/ingress`)
- Create: `apps/ingress/internal/ratelimit/ratelimit.go`, `ratelimit_test.go`
- Create: `apps/ingress/internal/routes/table.go`, `table_test.go`
- Create: `apps/ingress/internal/routes/poller.go`, `poller_test.go`

**Interfaces:**
- Consumes: the controller's `GET /internal/v1/ingress/routes` contract from Task 4 (JSON array of `{service_id, label, peer_ip, target_port}`, `ETag`, 304, bearer token).
- Produces (module `github.com/hanfour/bamboo/apps/ingress`):
  - `ratelimit.New(ratePerMinute, burst int) *Limiter`; `(*Limiter).Allow(key string) bool`, `RetryAfter(key string) time.Duration`, `Cleanup(idleFor time.Duration)`, `Len() int`
  - `routes.Route{ ServiceID, Label, PeerIP string; TargetPort int }` (JSON tags as above); `(Route).Upstream() string`
  - `routes.NewTable(suffix string) *Table`; `(*Table).Lookup(host string) (*Entry, bool)`, `Replace(rs []Route)`, `Len() int`
  - `routes.Entry` with `Route Route` field and `Context() context.Context` (cancelled when the route leaves the table or its upstream changes)
  - `routes.PollerOptions{ ControllerURL, TokenPath string; Table *Table; Interval time.Duration; Client *http.Client; Log *slog.Logger; OnResult func(n int, err error) }`
  - `routes.NewPoller(o PollerOptions) *Poller`; `(*Poller).Refresh(ctx) error`, `(*Poller).Run(ctx)`
  - `routes.ReadToken(path string) (string, error)` (shared with the telemetry sink)

- [ ] **Step 1: Create the module and add it to the workspace**

`apps/ingress/go.mod`:

```
module github.com/hanfour/bamboo/apps/ingress

go 1.25.7
```

`go.work` becomes:

```
go 1.25.7

use (
	./apps/controller
	./apps/ingress
	./apps/relay
	./clients/cli
	./clients/core
	./proto
)
```

Run: `cd /Users/hanfourhuang/Projects/bamboo && go work sync && go list ./apps/ingress/... 2>&1 | head -1`
Expected: `go: warning: "./apps/ingress/..." matched no packages` (module exists, no code yet).

- [ ] **Step 2: Copy the rate limiter (Go's `internal/` rule forbids importing the controller's)**

`apps/ingress/internal/ratelimit/ratelimit.go` — the body is identical to `apps/controller/internal/ratelimit/ratelimit.go`; only the package comment differs. Keep the two in sync by hand (tiny, stable file).

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ratelimit is an in-memory per-key token bucket. It is a copy of
// apps/controller/internal/ratelimit: Go's internal/ rule keeps another
// module from importing that package. Keep the two identical.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a token-bucket rate limiter keyed by an arbitrary string.
// Each key gets its own bucket that starts full (burst) and refills at
// `rate` tokens/second up to `burst`.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64 // bucket capacity
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a Limiter allowing `burst` requests immediately and then
// `ratePerMinute` sustained requests per minute per key. Values below 1
// are clamped to 1 so a misconfiguration cannot disable limiting.
func New(ratePerMinute, burst int) *Limiter {
	if ratePerMinute < 1 {
		ratePerMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    float64(ratePerMinute) / 60.0,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consumes one token for key and reports whether the request is
// permitted.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// RetryAfter returns how long until a token for key is available.
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil || b.tokens >= 1 {
		return 0
	}
	need := 1 - b.tokens
	return time.Duration(need/l.rate*float64(time.Second)) + time.Millisecond
}

// Cleanup evicts buckets that have refilled and been idle for idleFor.
func (l *Limiter) Cleanup(idleFor time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, b := range l.buckets {
		idle := now.Sub(b.last)
		refilled := b.tokens+idle.Seconds()*l.rate >= l.burst
		if refilled && idle >= idleFor {
			delete(l.buckets, key)
		}
	}
}

// Len reports the number of tracked keys.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
```

`apps/ingress/internal/ratelimit/ratelimit_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter_BurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(60, 2) // 1 token/s, burst 2
	l.now = func() time.Time { return now }
	if !l.Allow("k") || !l.Allow("k") {
		t.Fatal("burst of 2 must pass")
	}
	if l.Allow("k") {
		t.Fatal("third request inside the same instant must be limited")
	}
	if ra := l.RetryAfter("k"); ra <= 0 || ra > 2*time.Second {
		t.Errorf("RetryAfter = %v", ra)
	}
	if !l.Allow("other") {
		t.Error("keys are independent")
	}
	now = now.Add(time.Second)
	if !l.Allow("k") {
		t.Error("one token must refill after 1s")
	}
	now = now.Add(time.Hour)
	l.Cleanup(time.Minute)
	if l.Len() != 0 {
		t.Errorf("Cleanup left %d keys", l.Len())
	}
}
```

Run: `cd apps/ingress && go test -race ./internal/ratelimit/ -count=1` → `ok`.

- [ ] **Step 3: Write the failing table tests**

`apps/ingress/internal/routes/table_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import "testing"

func testRoute(label, ip string, port int) Route {
	return Route{ServiceID: "sid-" + label, Label: label, PeerIP: ip, TargetPort: port}
}

func TestTable_LookupNormalizesHost(t *testing.T) {
	tb := NewTable("svc.example.test")
	tb.Replace([]Route{testRoute("app", "100.64.0.5", 47800)})
	for _, host := range []string{"app.svc.example.test", "APP.svc.Example.test", "app.svc.example.test:443", "app.svc.example.test."} {
		e, ok := tb.Lookup(host)
		if !ok || e.Route.Upstream() != "100.64.0.5:47800" {
			t.Errorf("Lookup(%q) = %v, %v", host, e, ok)
		}
	}
	for _, host := range []string{"other.svc.example.test", "app.example.test", "svc.example.test", ""} {
		if _, ok := tb.Lookup(host); ok {
			t.Errorf("Lookup(%q) matched", host)
		}
	}
}

func TestTable_ReplaceSkipsInvalidRoutes(t *testing.T) {
	tb := NewTable("svc.example.test")
	tb.Replace([]Route{
		testRoute("ok", "100.64.0.5", 47800),
		testRoute("badip", "nope", 47800),
		testRoute("badport", "100.64.0.6", 0),
		testRoute("Bad.Label", "100.64.0.7", 47800),
	})
	if tb.Len() != 1 {
		t.Errorf("Len = %d, want 1", tb.Len())
	}
}

func TestTable_ReplaceCancelsRemovedAndChangedOnly(t *testing.T) {
	tb := NewTable("svc.example.test")
	tb.Replace([]Route{testRoute("keep", "100.64.0.5", 1), testRoute("move", "100.64.0.6", 2), testRoute("gone", "100.64.0.7", 3)})
	keep, _ := tb.Lookup("keep.svc.example.test")
	move, _ := tb.Lookup("move.svc.example.test")
	gone, _ := tb.Lookup("gone.svc.example.test")

	tb.Replace([]Route{testRoute("keep", "100.64.0.5", 1), testRoute("move", "100.64.0.6", 9)})

	if keep.Context().Err() != nil {
		t.Error("unchanged route must keep its context")
	}
	if move.Context().Err() == nil || gone.Context().Err() == nil {
		t.Error("changed and removed routes must be cancelled")
	}
	if again, _ := tb.Lookup("keep.svc.example.test"); again != keep {
		t.Error("unchanged route must keep its entry")
	}
}
```

Run: `go test ./internal/routes/ -count=1` → FAIL `undefined: NewTable`.

- [ ] **Step 4: Implement `table.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package routes holds bamboo-ingress's Host → upstream table and the
// poller that refreshes it from the controller.
package routes

import (
	"context"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Route is one live public service, as served by the controller's
// GET /internal/v1/ingress/routes.
type Route struct {
	ServiceID  string `json:"service_id"`
	Label      string `json:"label"`
	PeerIP     string `json:"peer_ip"`
	TargetPort int    `json:"target_port"`
}

// Upstream is the mesh address to dial.
func (r Route) Upstream() string { return net.JoinHostPort(r.PeerIP, strconv.Itoa(r.TargetPort)) }

// Entry is a Route plus a context that is cancelled when the route
// leaves the table or its upstream changes, so in-flight streams end
// when a service is disabled.
type Entry struct {
	Route  Route
	ctx    context.Context
	cancel context.CancelFunc
}

// Context is cancelled when this entry is retired.
func (e *Entry) Context() context.Context { return e.ctx }

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Table maps public host names to entries. Safe for concurrent use.
type Table struct {
	suffix string
	mu     sync.RWMutex
	byHost map[string]*Entry
}

// NewTable returns an empty table for hosts "<label>.<suffix>".
func NewTable(suffix string) *Table {
	return &Table{suffix: strings.TrimSuffix(strings.ToLower(suffix), "."), byHost: map[string]*Entry{}}
}

// Lookup returns the entry for host (case-insensitive, port and trailing
// dot ignored). Exact match only.
func (t *Table) Lookup(host string) (*Entry, bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(h, ".")
	t.mu.RLock()
	defer t.mu.RUnlock()
	e, ok := t.byHost[h]
	return e, ok
}

// Replace installs rs as the new table. Unchanged routes keep their
// entry (and context); removed or changed ones are cancelled. Invalid
// routes are skipped.
func (t *Table) Replace(rs []Route) {
	t.mu.Lock()
	old := t.byHost
	next := make(map[string]*Entry, len(rs))
	for _, r := range rs {
		if !validRoute(r) {
			continue
		}
		host := r.Label + "." + t.suffix
		if e, ok := old[host]; ok && e.Route == r {
			next[host] = e
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		next[host] = &Entry{Route: r, ctx: ctx, cancel: cancel}
	}
	t.byHost = next
	t.mu.Unlock()
	for host, e := range old {
		if n, ok := next[host]; !ok || n != e {
			e.cancel()
		}
	}
}

// Len reports the number of routes.
func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byHost)
}

func validRoute(r Route) bool {
	if r.ServiceID == "" || !labelRE.MatchString(r.Label) || r.TargetPort < 1 || r.TargetPort > 65535 {
		return false
	}
	_, err := netip.ParseAddr(r.PeerIP)
	return err == nil
}
```

Run: `go test -race ./internal/routes/ -count=1` → `ok`.

- [ ] **Step 5: Write the failing poller tests**

`apps/ingress/internal/routes/poller_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func writeToken(t *testing.T, tok string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ingress.token")
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type fakeController struct {
	status    atomic.Int32 // 0 ⇒ 200 with routes
	sawAuth   atomic.Value
	sawIfNone atomic.Value
	routes    []Route
}

func (f *fakeController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.sawAuth.Store(r.Header.Get("Authorization"))
	f.sawIfNone.Store(r.Header.Get("If-None-Match"))
	if r.URL.Path != "/internal/v1/ingress/routes" {
		http.NotFound(w, r)
		return
	}
	if s := f.status.Load(); s != 0 {
		w.WriteHeader(int(s))
		return
	}
	if r.Header.Get("If-None-Match") == `"v1"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", `"v1"`)
	_ = json.NewEncoder(w).Encode(f.routes)
}

func newTestPoller(t *testing.T, url string) (*Poller, *Table) {
	tb := NewTable("svc.example.test")
	return NewPoller(PollerOptions{ControllerURL: url, TokenPath: writeToken(t, "tok-1"), Table: tb}), tb
}

func TestPoller_LoadsRoutesWithToken(t *testing.T) {
	fc := &fakeController{routes: []Route{{ServiceID: "s1", Label: "app", PeerIP: "100.64.0.5", TargetPort: 47800}}}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	p, tb := newTestPoller(t, srv.URL)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := fc.sawAuth.Load(); got != "Bearer tok-1" {
		t.Errorf("Authorization = %v", got)
	}
	if _, ok := tb.Lookup("app.svc.example.test"); !ok {
		t.Error("route not installed")
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if got := fc.sawIfNone.Load(); got != `"v1"` {
		t.Errorf("If-None-Match = %v, want \"v1\"", got)
	}
	if tb.Len() != 1 {
		t.Error("304 must keep the table")
	}
}

func TestPoller_KeepsLastTableOnErrors(t *testing.T) {
	fc := &fakeController{routes: []Route{{ServiceID: "s1", Label: "app", PeerIP: "100.64.0.5", TargetPort: 47800}}}
	srv := httptest.NewServer(fc)
	p, tb := newTestPoller(t, srv.URL)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.status.Store(http.StatusInternalServerError)
	if err := p.Refresh(context.Background()); err == nil {
		t.Error("500 must be an error")
	}
	srv.Close()
	if err := p.Refresh(context.Background()); err == nil {
		t.Error("unreachable controller must be an error")
	}
	if _, ok := tb.Lookup("app.svc.example.test"); !ok {
		t.Error("last good table must survive controller errors")
	}
}

func TestPoller_EmptyTokenIsAnError(t *testing.T) {
	tb := NewTable("svc.example.test")
	p := NewPoller(PollerOptions{ControllerURL: "http://127.0.0.1:1", TokenPath: writeToken(t, "  "), Table: tb})
	if err := p.Refresh(context.Background()); err == nil {
		t.Error("empty token file must fail")
	}
}
```

Run: `go test ./internal/routes/ -count=1` → FAIL `undefined: NewPoller`.

- [ ] **Step 6: Implement `poller.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultInterval is the spec's 10 s poll.
const DefaultInterval = 10 * time.Second

const maxRoutesBody = 4 << 20

// PollerOptions configures a Poller. Client, Log, Interval and OnResult
// are optional.
type PollerOptions struct {
	ControllerURL string
	TokenPath     string
	Table         *Table
	Interval      time.Duration
	Client        *http.Client
	Log           *slog.Logger
	OnResult      func(n int, err error)
}

// Poller refreshes a Table from the controller. On any failure the table
// is left as it was (spec: keep the last good table).
type Poller struct {
	o    PollerOptions
	etag string
}

// NewPoller fills defaults.
func NewPoller(o PollerOptions) *Poller {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.ControllerURL = strings.TrimSuffix(o.ControllerURL, "/")
	return &Poller{o: o}
}

// ReadToken reads the service token file. It is re-read on every call so
// a rotated token is picked up without a restart.
func ReadToken(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-configured path
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("token file is empty")
	}
	return tok, nil
}

// Refresh fetches the routes once.
func (p *Poller) Refresh(ctx context.Context) error {
	err := p.refresh(ctx)
	if p.o.OnResult != nil {
		p.o.OnResult(p.o.Table.Len(), err)
	}
	return err
}

func (p *Poller) refresh(ctx context.Context) error {
	tok, err := ReadToken(p.o.TokenPath)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.o.ControllerURL+"/internal/v1/ingress/routes", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if p.etag != "" {
		req.Header.Set("If-None-Match", p.etag)
	}
	resp, err := p.o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("routes: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("routes: controller responded %d", resp.StatusCode)
	}
	var rs []Route
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRoutesBody)).Decode(&rs); err != nil {
		return fmt.Errorf("routes: decode: %w", err)
	}
	p.o.Table.Replace(rs)
	p.etag = resp.Header.Get("ETag")
	return nil
}

// Run refreshes immediately and then every Interval until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	tick := time.NewTicker(p.o.Interval)
	defer tick.Stop()
	for {
		if err := p.Refresh(ctx); err != nil && ctx.Err() == nil {
			p.o.Log.Warn("routes refresh failed; keeping last table", "err", err, "routes", p.o.Table.Len())
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
```

- [ ] **Step 7: Run tests**

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/ingress && go vet ./... && go test -race ./... -count=1`
Expected: `ok` for `internal/ratelimit` and `internal/routes`.

- [ ] **Step 8: Commit**

```bash
cd /Users/hanfourhuang/Projects/bamboo
git add go.work apps/ingress
git commit -m "feat(ingress): new apps/ingress module with routes table, poller, and rate limiter

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Streaming reverse proxy with limits

**Files:**
- Create: `apps/ingress/internal/proxy/proxy.go`
- Create: `apps/ingress/internal/proxy/idle.go`
- Test: `apps/ingress/internal/proxy/proxy_test.go`

**Interfaces:**
- Consumes: `routes.Table`, `routes.Entry`, `(*Entry).Context()`, `Route.Upstream()` (Task 6); `ratelimit.New` (Task 6).
- Produces:
  - `proxy.Limits{ PerServicePerMinute, PerClientPerMinute int; MaxBodyBytes int64; DialTimeout, IdleTimeout time.Duration }`, `proxy.DefaultLimits() Limits` (120, 300, 8 MiB, 5 s, 10 min)
  - `proxy.Record{ ServiceID, Label, ClientIP string; Status int; BytesIn, BytesOut int64; Duration time.Duration; At time.Time }` (no body or header fields by construction)
  - `type proxy.Recorder interface { Record(Record) }`
  - `proxy.Options{ Table *routes.Table; Limits Limits; Recorder Recorder; Transport http.RoundTripper }`
  - `proxy.New(o Options) *Handler`; `(*Handler).ServeHTTP`; `(*Handler).CleanupLimiters(idle time.Duration)`
  - `proxy.ClientIP(r *http.Request) string` (last `X-Forwarded-For` entry set by Caddy, else `RemoteAddr`)

Behavior pinned by tests: exact Host routing; unknown host 404; upstream unreachable 502 `service unavailable`; per-client then per-service limit → 429 + `Retry-After`; body > cap → 413 (known length before dialing, chunked while streaming); SSE flushed per event; idle stream closed after `IdleTimeout`; route removal ends in-flight streams; outbound `X-Forwarded-For`/`-Proto: https`/`-Host` and `X-Bamboo-Ingress-Service` set (client-supplied values replaced); public `Host` preserved.

- [ ] **Step 1: Write the failing proxy tests**

`apps/ingress/internal/proxy/proxy_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
)

const testHost = "app.svc.test"

type memRecorder struct {
	mu   sync.Mutex
	recs []Record
}

func (m *memRecorder) Record(r Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
}

func (m *memRecorder) all() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Record(nil), m.recs...)
}

func routeTo(t *testing.T, upstreamURL string) routes.Route {
	t.Helper()
	hostPort := strings.TrimPrefix(upstreamURL, "http://")
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return routes.Route{ServiceID: "svc-1", Label: "app", PeerIP: host, TargetPort: port}
}

// setup returns a front server running the proxy for one route.
func setup(t *testing.T, upstream http.Handler, limits Limits) (*httptest.Server, *routes.Table, *memRecorder) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	tb := routes.NewTable("svc.test")
	tb.Replace([]routes.Route{routeTo(t, up.URL)})
	rec := &memRecorder{}
	front := httptest.NewServer(New(Options{Table: tb, Limits: limits, Recorder: rec}))
	t.Cleanup(front.Close)
	return front, tb, rec
}

func do(t *testing.T, front *httptest.Server, method, host, path string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, front.URL+path, body)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestProxy_RoutesByHostAndSetsHeaders(t *testing.T) {
	var got *http.Request
	front, _, rec := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("hello"))
	}), DefaultLimits())

	resp := do(t, front, http.MethodPost, testHost, "/mcp?x=1", strings.NewReader("ping"), map[string]string{
		"X-Forwarded-For":          "203.0.113.7",
		"X-Bamboo-Ingress-Service": "spoofed",
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if got.URL.Path != "/mcp" || got.URL.RawQuery != "x=1" || got.Host != testHost {
		t.Errorf("upstream saw path=%q query=%q host=%q", got.URL.Path, got.URL.RawQuery, got.Host)
	}
	for k, want := range map[string]string{
		"X-Forwarded-For":          "203.0.113.7",
		"X-Forwarded-Proto":        "https",
		"X-Forwarded-Host":         testHost,
		"X-Bamboo-Ingress-Service": "svc-1",
	} {
		if v := got.Header.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	recs := rec.all()
	if len(recs) != 1 || recs[0].ServiceID != "svc-1" || recs[0].ClientIP != "203.0.113.7" ||
		recs[0].Status != 200 || recs[0].BytesIn != 4 || recs[0].BytesOut != 5 {
		t.Errorf("record = %+v", recs)
	}
}

func TestProxy_UnknownHost404(t *testing.T) {
	front, _, rec := setup(t, http.NotFoundHandler(), DefaultLimits())
	resp := do(t, front, http.MethodGet, "nope.svc.test", "/", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
	if len(rec.all()) != 0 {
		t.Error("unknown hosts are not recorded")
	}
}

func TestProxy_UpstreamDown502(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	tb := routes.NewTable("svc.test")
	tb.Replace([]routes.Route{routeTo(t, "http://"+addr)})
	front := httptest.NewServer(New(Options{Table: tb, Limits: DefaultLimits()}))
	defer front.Close()
	resp := do(t, front, http.MethodGet, testHost, "/", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "service unavailable") {
		t.Errorf("status=%d body=%q, want 502 service unavailable", resp.StatusCode, body)
	}
}

func TestProxy_PerClientAndPerServiceLimits(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	lim := DefaultLimits()
	lim.PerClientPerMinute = 2
	front, _, _ := setup(t, ok, lim)
	for i := 0; i < 2; i++ {
		r := do(t, front, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100.1"})
		r.Body.Close()
	}
	r := do(t, front, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100.1"})
	r.Body.Close()
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Errorf("per-client: status=%d retry-after=%q", r.StatusCode, r.Header.Get("Retry-After"))
	}

	lim = DefaultLimits()
	lim.PerServicePerMinute = 2
	front2, _, _ := setup(t, ok, lim)
	for i := 0; i < 2; i++ {
		r := do(t, front2, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100." + strconv.Itoa(10+i)})
		r.Body.Close()
	}
	r = do(t, front2, http.MethodGet, testHost, "/", nil, map[string]string{"X-Forwarded-For": "198.51.100.99"})
	r.Body.Close()
	if r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("per-service: status=%d, want 429", r.StatusCode)
	}
}

func TestProxy_BodyCap413(t *testing.T) {
	var calls int
	var mu sync.Mutex
	front, _, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
	}), Limits{PerServicePerMinute: 100, PerClientPerMinute: 100, MaxBodyBytes: 16, DialTimeout: time.Second, IdleTimeout: time.Minute})

	big := strings.Repeat("x", 32)
	r := do(t, front, http.MethodPost, testHost, "/", strings.NewReader(big), nil)
	r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("known length: status=%d, want 413", r.StatusCode)
	}
	mu.Lock()
	if calls != 0 {
		t.Errorf("upstream called %d times for an over-cap known-length body", calls)
	}
	mu.Unlock()

	// Unknown length (chunked): the cap trips while streaming.
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte(big)); pw.Close() }()
	r = do(t, front, http.MethodPost, testHost, "/", pr, nil)
	r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked: status=%d, want 413", r.StatusCode)
	}
}

// sseUpstream writes one event, then waits for release before the next.
func sseUpstream(release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = w.Write([]byte("data: two\n\n"))
		case <-r.Context().Done():
		}
	})
}

func readLine(t *testing.T, br *bufio.Reader, within time.Duration) (string, error) {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := br.ReadString('\n'); ch <- res{s, err} }()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(within):
		t.Fatalf("no line within %v", within)
		return "", nil
	}
}

func TestProxy_StreamsSSEUnbuffered(t *testing.T) {
	release := make(chan struct{})
	front, _, _ := setup(t, sseUpstream(release), DefaultLimits())
	resp := do(t, front, http.MethodGet, testHost, "/events", nil, nil)
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if line, err := readLine(t, br, 2*time.Second); err != nil || line != "data: one\n" {
		t.Fatalf("first event = %q, %v (buffered?)", line, err)
	}
	close(release)
	_, _ = readLine(t, br, 2*time.Second) // blank separator
	if line, _ := readLine(t, br, 2*time.Second); line != "data: two\n" {
		t.Errorf("second event = %q", line)
	}
}

func TestProxy_IdleTimeoutClosesStream(t *testing.T) {
	lim := DefaultLimits()
	lim.IdleTimeout = 150 * time.Millisecond
	front, _, _ := setup(t, sseUpstream(make(chan struct{})), lim)
	resp := do(t, front, http.MethodGet, testHost, "/events", nil, nil)
	defer resp.Body.Close()
	start := time.Now()
	_, err := io.ReadAll(resp.Body)
	if time.Since(start) > 3*time.Second {
		t.Errorf("idle stream lived %v", time.Since(start))
	}
	_ = err // EOF or unexpected EOF are both fine: the stream ended.
}

func TestProxy_RouteRemovalEndsStream(t *testing.T) {
	front, tb, _ := setup(t, sseUpstream(make(chan struct{})), DefaultLimits())
	resp := do(t, front, http.MethodGet, testHost, "/events", nil, nil)
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := readLine(t, br, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	tb.Replace(nil)
	done := make(chan struct{})
	go func() { _, _ = io.ReadAll(br); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream kept running after the route was removed")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/ingress && go test ./internal/proxy/ -count=1`
Expected: FAIL to compile, `undefined: New` / `DefaultLimits`.

- [ ] **Step 3: Implement `idle.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// idleTimer cancels a request when no bytes move in either direction for
// timeout. Touch is called from the response writer and the request body
// reader, which run on different goroutines, so the clock is atomic.
type idleTimer struct {
	timeout time.Duration
	last    atomic.Int64
	cancel  context.CancelFunc
	stop    chan struct{}
	once    sync.Once
}

func newIdleTimer(timeout time.Duration, cancel context.CancelFunc) *idleTimer {
	t := &idleTimer{timeout: timeout, cancel: cancel, stop: make(chan struct{})}
	t.Touch()
	go t.watch()
	return t
}

// Touch records activity.
func (t *idleTimer) Touch() { t.last.Store(time.Now().UnixNano()) }

// Stop ends the watcher. Safe to call more than once.
func (t *idleTimer) Stop() { t.once.Do(func() { close(t.stop) }) }

func (t *idleTimer) watch() {
	tick := t.timeout / 4
	if tick < 5*time.Millisecond {
		tick = 5 * time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case now := <-ticker.C:
			if now.UnixNano()-t.last.Load() >= int64(t.timeout) {
				t.cancel()
				return
			}
		}
	}
}
```

- [ ] **Step 4: Implement `proxy.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package proxy is bamboo-ingress's Host-routed reverse proxy
// (docs/design/2026-11-02-public-services-ingress.md §5).
package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/ratelimit"
	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
)

// Limits are the spec §5 limits.
type Limits struct {
	PerServicePerMinute int
	PerClientPerMinute  int
	MaxBodyBytes        int64
	DialTimeout         time.Duration
	IdleTimeout         time.Duration
}

// DefaultLimits: 120/min per service, 300/min per client IP, 8 MiB body,
// 5 s dial, 10 min idle stream.
func DefaultLimits() Limits {
	return Limits{
		PerServicePerMinute: 120,
		PerClientPerMinute:  300,
		MaxBodyBytes:        8 << 20,
		DialTimeout:         5 * time.Second,
		IdleTimeout:         10 * time.Minute,
	}
}

// Record is per-request metadata. It has no field that could carry a
// body or a header, so telemetry cannot leak either.
type Record struct {
	ServiceID string
	Label     string
	ClientIP  string
	Status    int
	BytesIn   int64
	BytesOut  int64
	Duration  time.Duration
	At        time.Time
}

// Recorder receives one Record per routed request. Must not block.
type Recorder interface {
	Record(Record)
}

// Options configure a Handler. Recorder and Transport are optional.
type Options struct {
	Table     *routes.Table
	Limits    Limits
	Recorder  Recorder
	Transport http.RoundTripper
}

// Handler is the public-facing http.Handler.
type Handler struct {
	table      *routes.Table
	limits     Limits
	rec        Recorder
	perService *ratelimit.Limiter
	perClient  *ratelimit.Limiter
	rp         *httputil.ReverseProxy
}

type stateKey struct{}

// reqState travels in the outbound request context from ServeHTTP to the
// ReverseProxy callbacks.
type reqState struct {
	entry    *routes.Entry
	clientIP string
	body     *countingBody
}

// New builds a Handler.
func New(o Options) *Handler {
	h := &Handler{
		table:      o.Table,
		limits:     o.Limits,
		rec:        o.Recorder,
		perService: ratelimit.New(o.Limits.PerServicePerMinute, o.Limits.PerServicePerMinute),
		perClient:  ratelimit.New(o.Limits.PerClientPerMinute, o.Limits.PerClientPerMinute),
	}
	transport := o.Transport
	if transport == nil {
		transport = &http.Transport{
			DialContext:         (&net.Dialer{Timeout: o.Limits.DialTimeout, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	h.rp = &httputil.ReverseProxy{
		Rewrite:       rewrite,
		Transport:     transport,
		FlushInterval: -1, // flush every write: SSE and chunked MCP responses
		ErrorHandler:  errorHandler,
	}
	return h
}

func rewrite(pr *httputil.ProxyRequest) {
	st := pr.In.Context().Value(stateKey{}).(*reqState)
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = st.entry.Route.Upstream()
	pr.Out.Host = pr.In.Host
	// Rewrite runs after ReverseProxy strips inbound X-Forwarded-*.
	pr.Out.Header.Set("X-Forwarded-For", st.clientIP)
	pr.Out.Header.Set("X-Forwarded-Proto", "https")
	pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
	pr.Out.Header.Set("X-Bamboo-Ingress-Service", st.entry.Route.ServiceID)
}

func errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := r.Context().Value(stateKey{}).(*reqState)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || (st != nil && st.body.tooLarge.Load()) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "service unavailable", http.StatusBadGateway)
}

// ServeHTTP routes one request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.table.Lookup(r.Host)
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	start := time.Now()
	rw := &recorder{ResponseWriter: w}
	st := &reqState{entry: entry, clientIP: ClientIP(r)}
	defer h.report(st, rw, start)

	if !h.allow(rw, h.perClient, "ip:"+st.clientIP) || !h.allow(rw, h.perService, "svc:"+entry.Route.ServiceID) {
		return
	}
	if r.ContentLength > h.limits.MaxBodyBytes {
		http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stopRoute := context.AfterFunc(entry.Context(), cancel) // route disabled ⇒ end the stream
	defer stopRoute()
	idle := newIdleTimer(h.limits.IdleTimeout, cancel)
	defer idle.Stop()
	rw.idle = idle

	st.body = &countingBody{rc: http.MaxBytesReader(rw, r.Body, h.limits.MaxBodyBytes), idle: idle}
	out := r.WithContext(context.WithValue(ctx, stateKey{}, st))
	out.Body = st.body
	h.rp.ServeHTTP(rw, out)
}

func (h *Handler) allow(w http.ResponseWriter, lim *ratelimit.Limiter, key string) bool {
	if lim.Allow(key) {
		return true
	}
	secs := int(lim.RetryAfter(key).Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
	return false
}

func (h *Handler) report(st *reqState, rw *recorder, start time.Time) {
	if h.rec == nil {
		return
	}
	status := rw.status
	if status == 0 {
		status = 499 // client went away before any response
	}
	var in int64
	if st.body != nil {
		in = st.body.n.Load()
	}
	h.rec.Record(Record{
		ServiceID: st.entry.Route.ServiceID, Label: st.entry.Route.Label, ClientIP: st.clientIP,
		Status: status, BytesIn: in, BytesOut: rw.n.Load(), Duration: time.Since(start), At: start,
	})
}

// CleanupLimiters bounds limiter memory; call it periodically.
func (h *Handler) CleanupLimiters(idle time.Duration) {
	h.perService.Cleanup(idle)
	h.perClient.Cleanup(idle)
}

// ClientIP is the client address. The listener is reachable only from
// Caddy, which replaces X-Forwarded-For with the client IP, so the last
// entry is trusted; RemoteAddr is the fallback.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// recorder captures status and bytes out, and counts writes as activity.
type recorder struct {
	http.ResponseWriter
	status int
	n      atomic.Int64
	idle   *idleTimer
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.n.Add(int64(n))
	if r.idle != nil {
		r.idle.Touch()
	}
	return n, err
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// countingBody counts bytes in, marks activity, and remembers when the
// body cap tripped (the transport may wrap the MaxBytesError).
type countingBody struct {
	rc       io.ReadCloser
	n        atomic.Int64
	tooLarge atomic.Bool
	idle     *idleTimer
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.n.Add(int64(n))
	if n > 0 {
		b.idle.Touch()
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		b.tooLarge.Store(true)
	}
	return n, err
}

func (b *countingBody) Close() error { return b.rc.Close() }
```

Note `recorder.status` is written only on the handler goroutine (ReverseProxy writes the response there), so it needs no atomic; `n` and the body counter are read in `report` after the transport goroutine may have touched them, so they are atomic.

- [ ] **Step 5: Run tests (with the race detector, as CI does)**

Run: `go test -race ./internal/proxy/ -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: 8 × `--- PASS` (`RoutesByHostAndSetsHeaders`, `UnknownHost404`, `UpstreamDown502`, `PerClientAndPerServiceLimits`, `BodyCap413`, `StreamsSSEUnbuffered`, `IdleTimeoutClosesStream`, `RouteRemovalEndsStream`), then `ok`.

- [ ] **Step 6: Commit**

```bash
git add apps/ingress/internal/proxy
git commit -m "feat(ingress): host-routed streaming reverse proxy with rate, body, and idle limits

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Telemetry sink, metrics, binary, image, build wiring

**Files:**
- Create: `apps/ingress/internal/telemetry/sink.go`, `sink_test.go`
- Create: `apps/ingress/internal/metrics/metrics.go`, `metrics_test.go`
- Create: `apps/ingress/cmd/bamboo-ingress/config.go`, `config_test.go`, `main.go`
- Create: `apps/ingress/Dockerfile`
- Modify: `apps/ingress/go.mod` / create `go.sum` (prometheus client)
- Modify: `Makefile` (`build` target), `.github/workflows/images.yml` (ingress job + PR path filter)

**Interfaces:**
- Consumes: `proxy.Record`, `proxy.Recorder`, `proxy.New`, `proxy.DefaultLimits` (Task 7); `routes.NewTable`, `routes.NewPoller`, `routes.ReadToken`, `routes.DefaultInterval` (Task 6); controller `POST /internal/v1/ingress/events` (Task 5).
- Produces:
  - `telemetry.SinkOptions{ ControllerURL, TokenPath string; Client *http.Client; QueueSize, BatchSize int; FlushEvery time.Duration; Log *slog.Logger; OnDrop func(n int) }`
  - `telemetry.NewSink(o SinkOptions) *Sink`; `(*Sink).Record(proxy.Record)` (non-blocking), `(*Sink).Run(ctx)`, `(*Sink).Dropped() uint64`
  - `metrics.New(version string) *Metrics`; fields `Requests *prometheus.CounterVec`, `Routes prometheus.Gauge`, `RefreshFailures prometheus.Counter`, `LastRefresh prometheus.Gauge`, `TelemetryDropped prometheus.Counter`; `(*Metrics).Handler() http.Handler`; `(*Metrics).Recorder(next proxy.Recorder) proxy.Recorder`; `(*Metrics).OnRefresh(n int, err error)`
  - binary `bamboo-ingress`, env: `BAMBOO_INGRESS_SUFFIX` (required), `BAMBOO_INGRESS_LISTEN` (default `172.17.0.1:8090`), `BAMBOO_INGRESS_ADMIN_LISTEN` (default `127.0.0.1:8091`), `BAMBOO_CONTROLLER_URL` (default `http://127.0.0.1:8081`), `BAMBOO_INGRESS_TOKEN_FILE` (default `/run/bamboo/ingress.token`)
  - image `ghcr.io/hanfour/bamboo-ingress:<tag>`

- [ ] **Step 1: Add the prometheus dependency**

Run:
```bash
cd /Users/hanfourhuang/Projects/bamboo/apps/ingress
go get github.com/prometheus/client_golang@v1.23.2
```
Expected: `go: added github.com/prometheus/client_golang v1.23.2` (same version the controller uses, so the workspace resolves one copy). `go mod tidy` runs in Step 9 after code imports it.

- [ ] **Step 2: Write the failing sink test**

`apps/ingress/internal/telemetry/sink_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
)

func tokenFile(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(p, []byte("tok-1"), 0o600)
	return p
}

func TestSink_BatchesToController(t *testing.T) {
	got := make(chan []byte, 4)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/ingress/events" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		got <- b
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	s := NewSink(SinkOptions{ControllerURL: srv.URL, TokenPath: tokenFile(t), FlushEvery: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	for i := 0; i < 3; i++ {
		s.Record(proxy.Record{ServiceID: "svc-1", ClientIP: "203.0.113.7", Status: 200, BytesIn: 1, BytesOut: 2, Duration: 1500 * time.Millisecond, At: time.Now()})
	}
	var body []byte
	select {
	case body = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no batch posted")
	}
	if auth != "Bearer tok-1" {
		t.Errorf("Authorization = %q", auth)
	}
	var payload struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Events) != 3 {
		t.Fatalf("payload = %s (%v)", body, err)
	}
	e := payload.Events[0]
	if e["service_id"] != "svc-1" || e["status"].(float64) != 200 || e["duration_ms"].(float64) != 1500 {
		t.Errorf("event = %v", e)
	}
	for k := range e {
		if strings.Contains(strings.ToLower(k), "body") || strings.Contains(strings.ToLower(k), "authorization") {
			t.Errorf("telemetry carries %q", k)
		}
	}
}

func TestSink_DropsWhenFull(t *testing.T) {
	s := NewSink(SinkOptions{ControllerURL: "http://127.0.0.1:1", TokenPath: tokenFile(t), QueueSize: 1})
	for i := 0; i < 5; i++ {
		s.Record(proxy.Record{ServiceID: "svc-1", Status: 200})
	}
	if s.Dropped() != 4 {
		t.Errorf("Dropped = %d, want 4 (Record must never block a request)", s.Dropped())
	}
}
```

Run: `go test ./internal/telemetry/ -count=1` → FAIL `undefined: NewSink`.

- [ ] **Step 3: Implement `sink.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telemetry ships per-request metadata to the controller, which
// stores it in ClickHouse connection_events. Bodies and headers never
// enter this package: proxy.Record has no field for them.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
)

// SinkOptions configure a Sink. Zero values get defaults.
type SinkOptions struct {
	ControllerURL string
	TokenPath     string
	Client        *http.Client
	QueueSize     int
	BatchSize     int
	FlushEvery    time.Duration
	Log           *slog.Logger
	OnDrop        func(n int)
}

// Sink buffers records and posts them in batches.
type Sink struct {
	o       SinkOptions
	ch      chan proxy.Record
	dropped atomic.Uint64
}

type eventJSON struct {
	ServiceID  string    `json:"service_id"`
	ClientIP   string    `json:"client_ip"`
	Status     int       `json:"status"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	DurationMs int64     `json:"duration_ms"`
	OccurredAt time.Time `json:"occurred_at"`
}

// NewSink fills defaults: queue 4096, batch 500, flush every 5 s.
func NewSink(o SinkOptions) *Sink {
	if o.QueueSize <= 0 {
		o.QueueSize = 4096
	}
	if o.BatchSize <= 0 || o.BatchSize > 1000 {
		o.BatchSize = 500
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = 5 * time.Second
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.ControllerURL = strings.TrimSuffix(o.ControllerURL, "/")
	return &Sink{o: o, ch: make(chan proxy.Record, o.QueueSize)}
}

// Record enqueues r, dropping it when the queue is full. Never blocks.
func (s *Sink) Record(r proxy.Record) {
	select {
	case s.ch <- r:
	default:
		s.dropped.Add(1)
		if s.o.OnDrop != nil {
			s.o.OnDrop(1)
		}
	}
}

// Dropped reports records lost to a full queue or a failed post.
func (s *Sink) Dropped() uint64 { return s.dropped.Load() }

// Run batches until ctx ends, then flushes what it holds once.
func (s *Sink) Run(ctx context.Context) {
	tick := time.NewTicker(s.o.FlushEvery)
	defer tick.Stop()
	batch := make([]proxy.Record, 0, s.o.BatchSize)
	flush := func(c context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := s.post(c, batch); err != nil {
			s.dropped.Add(uint64(len(batch)))
			if s.o.OnDrop != nil {
				s.o.OnDrop(len(batch))
			}
			s.o.Log.Warn("telemetry post failed; batch dropped", "n", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			flush(shutdown)
			cancel()
			return
		case r := <-s.ch:
			batch = append(batch, r)
			if len(batch) >= s.o.BatchSize {
				flush(ctx)
			}
		case <-tick.C:
			flush(ctx)
		}
	}
}

func (s *Sink) post(ctx context.Context, batch []proxy.Record) error {
	tok, err := routes.ReadToken(s.o.TokenPath)
	if err != nil {
		return err
	}
	events := make([]eventJSON, 0, len(batch))
	for _, r := range batch {
		events = append(events, eventJSON{
			ServiceID: r.ServiceID, ClientIP: r.ClientIP, Status: r.Status, BytesIn: r.BytesIn,
			BytesOut: r.BytesOut, DurationMs: r.Duration.Milliseconds(), OccurredAt: r.At.UTC(),
		})
	}
	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.o.ControllerURL+"/internal/v1/ingress/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.o.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("controller responded %d", resp.StatusCode)
	}
	return nil
}
```

Run: `go test -race ./internal/telemetry/ -count=1` → `ok`.

- [ ] **Step 4: Write the failing metrics test**

`apps/ingress/internal/metrics/metrics_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
)

type nopRecorder struct{ n int }

func (r *nopRecorder) Record(proxy.Record) { r.n++ }

func TestMetrics_RecorderAndRefresh(t *testing.T) {
	m := New("test")
	next := &nopRecorder{}
	rec := m.Recorder(next)
	rec.Record(proxy.Record{Label: "app", Status: 200})
	rec.Record(proxy.Record{Label: "app", Status: 429})
	m.OnRefresh(3, nil)
	m.OnRefresh(3, errors.New("down"))
	if next.n != 2 {
		t.Errorf("next recorder saw %d records, want 2", next.n)
	}
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{
		`bamboo_ingress_requests_total{code="2xx",service="app"} 1`,
		`bamboo_ingress_requests_total{code="4xx",service="app"} 1`,
		`bamboo_ingress_routes 3`,
		`bamboo_ingress_routes_refresh_failures_total 1`,
		`bamboo_ingress_build_info{version="test"} 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
```

Run: `go test ./internal/metrics/ -count=1` → FAIL `undefined: New`.

- [ ] **Step 5: Implement `metrics.go`**

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package metrics is bamboo-ingress's Prometheus surface, served on the
// admin listener at /metrics.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the collectors. Labels are bounded: service is the
// route label (one per live service), code is the status family.
type Metrics struct {
	Registry         *prometheus.Registry
	Requests         *prometheus.CounterVec
	Duration         *prometheus.HistogramVec
	Routes           prometheus.Gauge
	RefreshFailures  prometheus.Counter
	LastRefresh      prometheus.Gauge
	TelemetryDropped prometheus.Counter
}

// New registers every collector on a fresh registry.
func New(version string) *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bamboo_ingress_requests_total", Help: "Routed requests by service and status family.",
		}, []string{"service", "code"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "bamboo_ingress_request_duration_seconds", Help: "Request duration, including streams.",
			Buckets: []float64{0.05, 0.25, 1, 5, 30, 120, 600},
		}, []string{"service"}),
		Routes:           prometheus.NewGauge(prometheus.GaugeOpts{Name: "bamboo_ingress_routes", Help: "Routes in the current table."}),
		RefreshFailures:  prometheus.NewCounter(prometheus.CounterOpts{Name: "bamboo_ingress_routes_refresh_failures_total", Help: "Failed routes refreshes (last table kept)."}),
		LastRefresh:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "bamboo_ingress_routes_last_success_timestamp_seconds", Help: "Unix time of the last successful refresh."}),
		TelemetryDropped: prometheus.NewCounter(prometheus.CounterOpts{Name: "bamboo_ingress_telemetry_dropped_total", Help: "Telemetry records dropped."}),
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "bamboo_ingress_build_info", Help: "Build version."}, []string{"version"})
	build.WithLabelValues(version).Set(1)
	m.Registry.MustRegister(m.Requests, m.Duration, m.Routes, m.RefreshFailures, m.LastRefresh, m.TelemetryDropped, build)
	return m
}

// Handler serves the registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// OnRefresh is a routes.PollerOptions.OnResult callback.
func (m *Metrics) OnRefresh(n int, err error) {
	m.Routes.Set(float64(n))
	if err != nil {
		m.RefreshFailures.Inc()
		return
	}
	m.LastRefresh.Set(float64(time.Now().Unix()))
}

type chain struct {
	m    *Metrics
	next proxy.Recorder
}

func (c chain) Record(r proxy.Record) {
	c.m.Requests.WithLabelValues(r.Label, strconv.Itoa(r.Status/100)+"xx").Inc()
	c.m.Duration.WithLabelValues(r.Label).Observe(r.Duration.Seconds())
	if c.next != nil {
		c.next.Record(r)
	}
}

// Recorder counts each record, then passes it to next.
func (m *Metrics) Recorder(next proxy.Recorder) proxy.Recorder { return chain{m: m, next: next} }
```

Run: `go test -race ./internal/metrics/ -count=1` → `ok`.

- [ ] **Step 6: Write the failing config test**

`apps/ingress/cmd/bamboo-ingress/config_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig_Defaults(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{"BAMBOO_INGRESS_SUFFIX": "SVC.miilink.net."}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Suffix != "svc.miilink.net" || c.Listen != "172.17.0.1:8090" || c.AdminListen != "127.0.0.1:8091" ||
		c.ControllerURL != "http://127.0.0.1:8081" || c.TokenFile != "/run/bamboo/ingress.token" {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadConfig_Rejects(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no suffix":      {},
		"bad url":        {"BAMBOO_INGRESS_SUFFIX": "svc.test", "BAMBOO_CONTROLLER_URL": "ftp://x"},
		"leading dot":    {"BAMBOO_INGRESS_SUFFIX": ".svc.test"},
		"listen no port": {"BAMBOO_INGRESS_SUFFIX": "svc.test", "BAMBOO_INGRESS_LISTEN": "172.17.0.1"},
	} {
		if _, err := loadConfig(envOf(env)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
```

Run: `go test ./cmd/bamboo-ingress/ -count=1` → FAIL `undefined: loadConfig`.

- [ ] **Step 7: Implement `config.go` and `main.go`**

`apps/ingress/cmd/bamboo-ingress/config.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type config struct {
	Suffix        string
	Listen        string
	AdminListen   string
	ControllerURL string
	TokenFile     string
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

// loadConfig reads the BAMBOO_INGRESS_* environment. Only the suffix is
// required; the defaults match infra/full.
func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		Suffix:        strings.TrimSuffix(strings.ToLower(strings.TrimSpace(getenv("BAMBOO_INGRESS_SUFFIX"))), "."),
		Listen:        envOr(getenv, "BAMBOO_INGRESS_LISTEN", "172.17.0.1:8090"),
		AdminListen:   envOr(getenv, "BAMBOO_INGRESS_ADMIN_LISTEN", "127.0.0.1:8091"),
		ControllerURL: envOr(getenv, "BAMBOO_CONTROLLER_URL", "http://127.0.0.1:8081"),
		TokenFile:     envOr(getenv, "BAMBOO_INGRESS_TOKEN_FILE", "/run/bamboo/ingress.token"),
	}
	if c.Suffix == "" || strings.HasPrefix(c.Suffix, ".") || !strings.Contains(c.Suffix, ".") {
		return c, errors.New("BAMBOO_INGRESS_SUFFIX must be a domain such as svc.miilink.net")
	}
	u, err := url.Parse(c.ControllerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, fmt.Errorf("BAMBOO_CONTROLLER_URL %q must be an http(s) URL", c.ControllerURL)
	}
	for _, addr := range []string{c.Listen, c.AdminListen} {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return c, fmt.Errorf("listen address %q: %w", addr, err)
		}
	}
	return c, nil
}
```

`apps/ingress/cmd/bamboo-ingress/main.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command bamboo-ingress serves public services
// (docs/design/2026-11-02-public-services-ingress.md §5): Caddy terminates
// TLS and forwards here; requests are routed by Host to a peer's mesh IP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/metrics"
	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
	"github.com/hanfour/bamboo/apps/ingress/internal/telemetry"
)

// Version is set with -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	m := metrics.New(Version)
	table := routes.NewTable(cfg.Suffix)
	poller := routes.NewPoller(routes.PollerOptions{ControllerURL: cfg.ControllerURL, TokenPath: cfg.TokenFile, Table: table, Log: log, OnResult: m.OnRefresh})
	sink := telemetry.NewSink(telemetry.SinkOptions{ControllerURL: cfg.ControllerURL, TokenPath: cfg.TokenFile, Log: log,
		OnDrop: func(n int) { m.TelemetryDropped.Add(float64(n)) }})
	handler := proxy.New(proxy.Options{Table: table, Limits: proxy.DefaultLimits(), Recorder: m.Recorder(sink)})

	go poller.Run(ctx)
	go sink.Run(ctx)
	go cleanupLoop(ctx, handler)

	public := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	admin := &http.Server{Addr: cfg.AdminListen, Handler: adminMux(m), ReadHeaderTimeout: 5 * time.Second}
	go serve(log, admin)
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = public.Shutdown(shutdown)
		_ = admin.Shutdown(shutdown)
	}()
	log.Info("bamboo-ingress listening", "addr", cfg.Listen, "admin", cfg.AdminListen, "suffix", cfg.Suffix, "version", Version)
	serve(log, public)
}

func serve(log *slog.Logger, s *http.Server) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("listen", "addr", s.Addr, "err", err)
		os.Exit(1)
	}
}

func adminMux(m *metrics.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": Version, "service": "bamboo-ingress"})
	})
	mux.Handle("/metrics", m.Handler())
	return mux
}

func cleanupLoop(ctx context.Context, h *proxy.Handler) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.CleanupLimiters(5 * time.Minute)
		}
	}
}
```

- [ ] **Step 8: Dockerfile**

`apps/ingress/Dockerfile`:

```dockerfile
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# bamboo-ingress: distroless static image. Built standalone (no go.work),
# like the relay. Runs with network_mode: host in infra/full.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY apps/ingress ./apps/ingress
WORKDIR /src/apps/ingress
ARG VERSION=dev
ENV GOWORK=off
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /out/bamboo-ingress ./cmd/bamboo-ingress

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bamboo-ingress /bamboo-ingress
USER nonroot
ENTRYPOINT ["/bamboo-ingress"]
```

- [ ] **Step 9: Build wiring**

Run `cd apps/ingress && go mod tidy` (creates/updates `go.sum`).

`Makefile` `build` target — add after the relay block:

```make
	@if [ -d apps/ingress/cmd/bamboo-ingress ]; then \
	  echo "==> build bamboo-ingress"; \
	  (cd apps/ingress && go build -o ../../bin/bamboo-ingress ./cmd/bamboo-ingress); \
	fi
```

`.github/workflows/images.yml`: add `- "apps/ingress/**"` to `on.pull_request.paths`, and a job (copy of the `relay` job with these differences):

```yaml
  ingress:
    name: ingress image
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: docker/setup-buildx-action@v3

      - name: Log in to GHCR
        if: github.event_name != 'pull_request'
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Compute tags
        id: tags
        run: |
          if [[ "${GITHUB_REF}" == refs/tags/v* ]]; then
            VERSION="${GITHUB_REF#refs/tags/}"
            echo "tags=ghcr.io/${{ github.repository_owner }}/bamboo-ingress:${VERSION},ghcr.io/${{ github.repository_owner }}/bamboo-ingress:latest" >> $GITHUB_OUTPUT
          else
            echo "tags=ghcr.io/${{ github.repository_owner }}/bamboo-ingress:main,ghcr.io/${{ github.repository_owner }}/bamboo-ingress:${GITHUB_SHA::7}" >> $GITHUB_OUTPUT
          fi

      - uses: docker/build-push-action@v5
        with:
          context: .
          file: apps/ingress/Dockerfile
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.tags.outputs.tags }}
          build-args: |
            VERSION=${{ github.ref_name }}
          cache-from: type=gha,scope=ingress
          cache-to: type=gha,scope=ingress,mode=max
```

- [ ] **Step 10: Verify**

Run:
```bash
cd /Users/hanfourhuang/Projects/bamboo
(cd apps/ingress && go vet ./... && go test -race ./... -count=1)
make build && ls bin/bamboo-ingress
make lint
make license-check
docker build -f apps/ingress/Dockerfile -t bamboo-ingress:dev .
docker run -d --name ingress-smoke -e BAMBOO_INGRESS_SUFFIX=svc.example.test -e BAMBOO_INGRESS_LISTEN=127.0.0.1:18090 bamboo-ingress:dev
sleep 2; docker inspect -f '{{.State.Running}}' ingress-smoke; docker logs ingress-smoke 2>&1 | head -3
docker rm -f ingress-smoke
```
Expected: all ingress packages `ok`; `bin/bamboo-ingress` exists; lint prints `0 issues.` for every module; license check prints `OK: … files all have SPDX headers.` and no boundary violations; the image builds; `docker inspect` prints `true` (it keeps running with no controller) and the logs show `bamboo-ingress listening` and `routes refresh failed; keeping last table`.

- [ ] **Step 11: Commit**

```bash
git add apps/ingress Makefile .github/workflows/images.yml
git commit -m "feat(ingress): telemetry sink, metrics, bamboo-ingress binary and image

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Web UI — `/ingress` page

**Files:**
- Create: `apps/web/src/lib/ingress.ts`, `apps/web/src/lib/ingress.test.ts`
- Create: `apps/web/src/lib/ingressActions.ts`
- Modify: `apps/web/src/lib/api.ts` (add `fetchIngress` after `fetchRelays`)
- Create: `apps/web/src/components/IngressView.tsx`, `apps/web/src/components/IngressView.test.tsx`
- Create: `apps/web/src/app/[locale]/ingress/page.tsx`
- Modify: `apps/web/src/app/[locale]/settings/page.tsx` (admin card next to `RelaysCard`)
- Modify: `apps/web/messages/en.json`, `apps/web/messages/zh-TW.json`

**Interfaces:**
- Consumes: REST from Tasks 3–4 (`GET /api/v1/ingress`, `POST /api/v1/peers/{id}/ingress {label,target_port}`, `POST /api/v1/ingress/{id}/approve`, `PATCH /api/v1/ingress/{id} {enabled}`, `DELETE /api/v1/ingress/{id}`, `POST /api/v1/ingress/token {peer_id}`); `fetchResult` (private in api.ts), `fetchPeers`, `fetchMe`; `CONTROLLER_BASE`, `controllerHeaders()` from `@/lib/controllerProxy`; `FetchErrorState`; `Peer` type (`id, hostname, ip, tags, approvalStatus`).
- Produces:
  - `type IngressService`, `type IngressTokenInfo`, `type IngressList`, `type IngressState = 'pending' | 'approved' | 'live' | 'peerPending'`
  - `validateIngressLabel(label: string): 'invalid' | 'reserved' | null`, `validateIngressPort(port: number): boolean`, `ingressState(s: IngressService): IngressState`
  - Server actions: `createIngressAction(peerId, label, targetPort)`, `approveIngressAction(id)`, `setIngressEnabledAction(id, enabled)`, `deleteIngressAction(id)` → `IngressActionResult`; `issueIngressTokenAction(peerId)` → `IngressTokenResult`
  - `fetchIngress(): Promise<FetchResult<IngressList>>`
  - `<IngressView list peers isAdmin />`

- [ ] **Step 1: Write the failing helper tests**

`apps/web/src/lib/ingress.test.ts`:

```ts
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import { ingressState, validateIngressLabel, validateIngressPort, type IngressService } from './ingress';

const base: IngressService = {
  id: 's1', peerId: 'p1', peerHostname: 'mac', peerIp: '100.64.0.5', label: 'yeschef',
  url: 'https://yeschef.svc.miilink.net', targetPort: 47800, enabled: false, approved: false,
  live: false, createdAt: '2026-11-02T00:00:00Z', updatedAt: '2026-11-02T00:00:00Z',
};

describe('validateIngressLabel', () => {
  it('accepts DNS labels up to 32 chars', () => {
    expect(validateIngressLabel('yeschef')).toBeNull();
    expect(validateIngressLabel('a-1')).toBeNull();
    expect(validateIngressLabel('a'.repeat(32))).toBeNull();
  });
  it('rejects bad shapes and reserved names', () => {
    for (const bad of ['', '-a', 'a-', 'A', 'a.b', 'a_b', 'a'.repeat(33)]) {
      expect(validateIngressLabel(bad)).toBe('invalid');
    }
    expect(validateIngressLabel('www')).toBe('reserved');
    expect(validateIngressLabel('ingress')).toBe('reserved');
  });
});

describe('validateIngressPort', () => {
  it('enforces 1024-65535', () => {
    expect(validateIngressPort(1024)).toBe(true);
    expect(validateIngressPort(65535)).toBe(true);
    expect(validateIngressPort(1023)).toBe(false);
    expect(validateIngressPort(65536)).toBe(false);
    expect(validateIngressPort(47800.5)).toBe(false);
  });
});

describe('ingressState', () => {
  it('maps the four states', () => {
    expect(ingressState(base)).toBe('pending');
    expect(ingressState({ ...base, approved: true })).toBe('approved');
    expect(ingressState({ ...base, approved: true, enabled: true, live: true })).toBe('live');
    expect(ingressState({ ...base, approved: true, enabled: true, live: false })).toBe('peerPending');
  });
});
```

Run: `cd /Users/hanfourhuang/Projects/bamboo/apps/web && npx vitest run src/lib/ingress.test.ts`
Expected: FAIL `Failed to resolve import "./ingress"`.

- [ ] **Step 2: Implement `ingress.ts`**

```ts
// SPDX-License-Identifier: AGPL-3.0-or-later

// Types and pure helpers for the public services ingress page
// (docs/design/2026-11-02-public-services-ingress.md §7). Mirrors the
// controller's ingressServiceJSON / ingressListJSON shapes.

export type IngressService = {
  id: string;
  peerId: string;
  peerHostname: string;
  peerIp: string;
  label: string;
  url: string; // "" when the controller has no suffix configured
  targetPort: number;
  enabled: boolean;
  approved: boolean;
  approvedAt?: string;
  live: boolean;
  aclRule?: string; // present only while live; generated, not editable
  createdAt: string;
  updatedAt: string;
};

export type IngressTokenInfo = { peerId: string; issuedAt: string; expiresAt: string };

export type IngressList = {
  services: IngressService[];
  suffix: string;
  token: IngressTokenInfo | null;
};

export type IngressState = 'pending' | 'approved' | 'live' | 'peerPending';

// Same rules as the controller (repo.ValidateIngressLabel); the
// controller stays the authority, this only gives instant feedback.
const LABEL_PATTERN = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;
const RESERVED = new Set(['www', 'api', 'admin', 'relay', 'controller', 'ingress', 'mail']);

export function validateIngressLabel(label: string): 'invalid' | 'reserved' | null {
  if (!LABEL_PATTERN.test(label)) return 'invalid';
  if (RESERVED.has(label)) return 'reserved';
  return null;
}

export function validateIngressPort(port: number): boolean {
  return Number.isInteger(port) && port >= 1024 && port <= 65535;
}

export function ingressState(s: IngressService): IngressState {
  if (s.live) return 'live';
  if (!s.approved) return 'pending';
  if (!s.enabled) return 'approved';
  return 'peerPending';
}
```

Run: `npx vitest run src/lib/ingress.test.ts` → PASS (3 suites).

- [ ] **Step 3: Server actions and fetcher**

`apps/web/src/lib/ingressActions.ts`:

```ts
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Server Actions for the /ingress page. Same session forwarding as
// lib/actions.ts, via controllerProxy's exported helpers ('use server'
// files may only export async functions, so nothing is shared from
// actions.ts).

'use server';

import { revalidatePath } from 'next/cache';
import { CONTROLLER_BASE, controllerHeaders } from '@/lib/controllerProxy';

export type IngressActionResult = { ok: true } | { ok: false; error: string };
export type IngressTokenResult =
  | { ok: true; token: string; expiresAt: string }
  | { ok: false; error: string };

async function send(method: string, path: string, body?: unknown): Promise<Response> {
  const headers = await controllerHeaders();
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  return fetch(`${CONTROLLER_BASE}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: 'no-store',
  });
}

async function run(method: string, path: string, body?: unknown): Promise<IngressActionResult> {
  try {
    const res = await send(method, path, body);
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/ingress', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

export async function createIngressAction(peerId: string, label: string, targetPort: number): Promise<IngressActionResult> {
  return run('POST', `/api/v1/peers/${encodeURIComponent(peerId)}/ingress`, {
    label: label.trim().toLowerCase(),
    target_port: targetPort,
  });
}

export async function approveIngressAction(id: string): Promise<IngressActionResult> {
  return run('POST', `/api/v1/ingress/${encodeURIComponent(id)}/approve`);
}

export async function setIngressEnabledAction(id: string, enabled: boolean): Promise<IngressActionResult> {
  return run('PATCH', `/api/v1/ingress/${encodeURIComponent(id)}`, { enabled });
}

export async function deleteIngressAction(id: string): Promise<IngressActionResult> {
  return run('DELETE', `/api/v1/ingress/${encodeURIComponent(id)}`);
}

// issueIngressTokenAction returns the token once; it is never readable again.
export async function issueIngressTokenAction(peerId: string): Promise<IngressTokenResult> {
  try {
    const res = await send('POST', '/api/v1/ingress/token', { peer_id: peerId });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    const body = (await res.json()) as { token: string; expiresAt: string };
    revalidatePath('/[locale]/ingress', 'page');
    return { ok: true, token: body.token, expiresAt: body.expiresAt };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}
```

In `apps/web/src/lib/api.ts`, add `import type { IngressList } from './ingress';` with the other imports, and after `fetchRelays`:

```ts
// fetchIngress lists the tenant's public services plus the suffix and
// the current ingress token's metadata (never the token itself).
export async function fetchIngress(): Promise<FetchResult<IngressList>> {
  const r = await fetchResult<IngressList>('/api/v1/ingress');
  if (r.kind !== 'ok') return r;
  return {
    kind: 'ok',
    value: { services: r.value.services ?? [], suffix: r.value.suffix ?? '', token: r.value.token ?? null },
  };
}
```

Run: `npm run typecheck` → exits 0.

- [ ] **Step 4: Write the failing component test**

`apps/web/src/components/IngressView.test.tsx`:

```tsx
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { IngressView } from './IngressView';
import {
  approveIngressAction,
  createIngressAction,
  issueIngressTokenAction,
} from '@/lib/ingressActions';
import type { IngressList } from '@/lib/ingress';
import type { Peer } from '@/lib/types';

vi.mock('next-intl', () => {
  const translate = (key: string) => key;
  return { useTranslations: () => translate };
});

vi.mock('@/lib/ingressActions', () => ({
  createIngressAction: vi.fn(async () => ({ ok: true as const })),
  approveIngressAction: vi.fn(async () => ({ ok: true as const })),
  setIngressEnabledAction: vi.fn(async () => ({ ok: true as const })),
  deleteIngressAction: vi.fn(async () => ({ ok: true as const })),
  issueIngressTokenAction: vi.fn(async () => ({ ok: true as const, token: 'tok-secret', expiresAt: '2027-11-02T00:00:00Z' })),
}));

const peers = [
  { id: 'p1', hostname: 'mac', ip: '100.64.0.5', tags: [], approvalStatus: 'approved' },
  { id: 'p2', hostname: 'vps', ip: '100.64.0.9', tags: ['ingress'], approvalStatus: 'approved' },
] as unknown as Peer[];

const list: IngressList = {
  suffix: 'svc.miilink.net',
  token: null,
  services: [{
    id: 's1', peerId: 'p1', peerHostname: 'mac', peerIp: '100.64.0.5', label: 'yeschef',
    url: 'https://yeschef.svc.miilink.net', targetPort: 47800, enabled: false, approved: false,
    live: false, createdAt: '2026-11-02T00:00:00Z', updatedAt: '2026-11-02T00:00:00Z',
  }],
};

describe('IngressView', () => {
  beforeEach(() => vi.clearAllMocks());

  it('shows a pending service and lets an admin approve it', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    expect(screen.getByText('https://yeschef.svc.miilink.net')).toBeInTheDocument();
    expect(screen.getByText('state.pending')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'actions.approve' }));
    expect(approveIngressAction).toHaveBeenCalledWith('s1');
  });

  it('hides admin actions from members', () => {
    render(<IngressView list={list} peers={peers} isAdmin={false} />);
    expect(screen.queryByRole('button', { name: 'actions.approve' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'token.issue' })).toBeNull();
  });

  it('validates the label before creating', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    await user.click(screen.getByRole('button', { name: 'form.openButton' }));
    await user.selectOptions(screen.getByLabelText('form.peerLabel'), 'p1');
    await user.type(screen.getByLabelText('form.labelLabel'), 'Bad_Label');
    await user.clear(screen.getByLabelText('form.portLabel'));
    await user.type(screen.getByLabelText('form.portLabel'), '47800');
    await user.click(screen.getByRole('button', { name: 'form.submit' }));
    expect(createIngressAction).not.toHaveBeenCalled();
    expect(screen.getByText('form.invalidLabel')).toBeInTheDocument();

    await user.clear(screen.getByLabelText('form.labelLabel'));
    await user.type(screen.getByLabelText('form.labelLabel'), 'notes');
    await user.click(screen.getByRole('button', { name: 'form.submit' }));
    expect(createIngressAction).toHaveBeenCalledWith('p1', 'notes', 47800);
  });

  it('issues a token for an ingress-tagged peer and shows it once', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    await user.selectOptions(screen.getByLabelText('token.peerLabel'), 'p2');
    await user.click(screen.getByRole('button', { name: 'token.issue' }));
    expect(issueIngressTokenAction).toHaveBeenCalledWith('p2');
    expect(await screen.findByText('tok-secret')).toBeInTheDocument();
  });
});
```

Run: `npx vitest run src/components/IngressView.test.tsx` → FAIL `Failed to resolve import "./IngressView"`.

- [ ] **Step 5: Implement `IngressView.tsx`**

```tsx
// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import {
  approveIngressAction,
  createIngressAction,
  deleteIngressAction,
  issueIngressTokenAction,
  setIngressEnabledAction,
  type IngressActionResult,
} from '@/lib/ingressActions';
import {
  ingressState,
  validateIngressLabel,
  validateIngressPort,
  type IngressList,
  type IngressService,
  type IngressState,
} from '@/lib/ingress';
import type { Peer } from '@/lib/types';

// IngressView is the public services admin surface (spec §7): list,
// create on a peer, approve (admin), enable/disable, delete, and
// issue/rotate the ingress service token. Mutations are server actions
// that revalidate the page, so the list re-renders from the controller.
export function IngressView({ list, peers, isAdmin }: { list: IngressList; peers: Peer[]; isAdmin: boolean }) {
  const t = useTranslations('ingress');
  const [error, setError] = useState<string | null>(null);
  const approved = peers.filter((p) => p.approvalStatus === 'approved');
  return (
    <div className="space-y-6">
      {error && (
        <div className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400">
          {t('errorPrefix')} {error}
        </div>
      )}
      <CreateForm peers={approved} onError={setError} />
      <ServiceTable services={list.services} isAdmin={isAdmin} onError={setError} />
      {isAdmin && <TokenPanel list={list} peers={approved.filter((p) => p.tags.includes('ingress'))} onError={setError} />}
    </div>
  );
}

const stateTone: Record<IngressState, string> = {
  live: 'border-bamboo-300/60 text-bamboo-200',
  approved: 'border-bamboo-200/30 text-bamboo-200/70',
  pending: 'border-amber-500/60 text-amber-400',
  peerPending: 'border-amber-500/60 text-amber-400',
};

function ServiceTable({ services, isAdmin, onError }: { services: IngressService[]; isAdmin: boolean; onError: (e: string | null) => void }) {
  const t = useTranslations('ingress');
  if (services.length === 0) {
    return (
      <p className="rounded-2xl border border-dashed border-[#777]/30 p-10 text-center text-[13px] text-bamboo-200/60">
        {t('empty')}
      </p>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="min-w-full divide-y divide-[#777]/20 text-[13px]">
        <thead className="bg-ink-900/40">
          <tr>
            {(['label', 'url', 'peer', 'port', 'state', 'actions'] as const).map((c) => (
              <th key={c} className="px-3 py-2 text-left text-xs font-medium text-bamboo-200/60">{t(`columns.${c}`)}</th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-[#777]/20">
          {services.map((s) => (
            <ServiceRow key={s.id} service={s} isAdmin={isAdmin} onError={onError} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ServiceRow({ service: s, isAdmin, onError }: { service: IngressService; isAdmin: boolean; onError: (e: string | null) => void }) {
  const t = useTranslations('ingress');
  const [pending, startTransition] = useTransition();
  const state = ingressState(s);
  const act = (fn: () => Promise<IngressActionResult>) =>
    startTransition(async () => {
      const res = await fn();
      onError(res.ok ? null : res.error);
    });
  return (
    <tr>
      <td className="px-3 py-2 font-mono text-bamboo-50">{s.label}</td>
      <td className="px-3 py-2 font-mono text-xs text-bamboo-200/80">{s.url || '—'}</td>
      <td className="px-3 py-2">{s.peerHostname} <span className="font-mono text-xs text-bamboo-200/50">{s.peerIp}</span></td>
      <td className="px-3 py-2 font-mono">{s.targetPort}</td>
      <td className="px-3 py-2">
        <span className={`inline-flex rounded-full border px-2 py-0.5 text-xs font-medium ${stateTone[state]}`}>{t(`state.${state}`)}</span>
        {s.aclRule && <div className="mt-1 font-mono text-[11px] text-bamboo-200/50" title={t('generatedRule')}>{s.aclRule}</div>}
      </td>
      <td className="space-x-2 px-3 py-2 whitespace-nowrap">
        {isAdmin && !s.approved && (
          <RowButton disabled={pending} onClick={() => act(() => approveIngressAction(s.id))}>{t('actions.approve')}</RowButton>
        )}
        {isAdmin && s.approved && (
          <RowButton disabled={pending} onClick={() => act(() => setIngressEnabledAction(s.id, !s.enabled))}>
            {s.enabled ? t('actions.disable') : t('actions.enable')}
          </RowButton>
        )}
        {isAdmin && (
          <RowButton disabled={pending} onClick={() => act(() => deleteIngressAction(s.id))}>{t('actions.delete')}</RowButton>
        )}
      </td>
    </tr>
  );
}

function CreateForm({ peers, onError }: { peers: Peer[]; onError: (e: string | null) => void }) {
  const tt = useTranslations('ingress');
  const t = (key: string) => tt(`form.${key}`);
  const [open, setOpen] = useState(false);
  const [peerId, setPeerId] = useState('');
  const [label, setLabel] = useState('');
  const [port, setPort] = useState('');
  const [hint, setHint] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  function submit() {
    const l = label.trim().toLowerCase();
    const p = Number(port);
    const bad = validateIngressLabel(l);
    if (bad) return setHint(bad === 'reserved' ? t('reservedLabel') : t('invalidLabel'));
    if (!validateIngressPort(p)) return setHint(t('invalidPort'));
    if (!peerId) return setHint(t('peerRequired'));
    setHint(null);
    startTransition(async () => {
      const res = await createIngressAction(peerId, l, p);
      onError(res.ok ? null : res.error);
      if (res.ok) {
        setOpen(false);
        setLabel('');
        setPort('');
      }
    });
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <button type="button" onClick={() => setOpen((v) => !v)}
          className="rounded-full bg-bamboo-50 px-4 py-1.5 text-[13px] font-medium text-ink-950 hover:bg-bamboo-100">
          {open ? t('cancel') : t('openButton')}
        </button>
      </div>
      {open && (
        <div className="grid gap-3 rounded-2xl bg-ink-900 p-4 sm:grid-cols-3">
          <Field id="ingress-peer" label={t('peerLabel')}>
            <select id="ingress-peer" value={peerId} onChange={(e) => setPeerId(e.target.value)} disabled={pending} className={inputCls}>
              <option value="">—</option>
              {peers.map((p) => <option key={p.id} value={p.id}>{p.hostname} ({p.ip})</option>)}
            </select>
          </Field>
          <Field id="ingress-label" label={t('labelLabel')} hint={t('labelHint')}>
            <input id="ingress-label" value={label} onChange={(e) => setLabel(e.target.value)} disabled={pending} className={inputCls} />
          </Field>
          <Field id="ingress-port" label={t('portLabel')} hint={t('portHint')}>
            <input id="ingress-port" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} disabled={pending} className={inputCls} />
          </Field>
          {hint && <p className="text-xs text-amber-400 sm:col-span-3">{hint}</p>}
          <div className="sm:col-span-3">
            <button type="button" onClick={submit} disabled={pending}
              className="rounded-full bg-bamboo-50 px-4 py-1.5 text-[13px] font-medium text-ink-950 hover:bg-bamboo-100">
              {t('submit')}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function TokenPanel({ list, peers, onError }: { list: IngressList; peers: Peer[]; onError: (e: string | null) => void }) {
  const tt = useTranslations('ingress');
  const t = (key: string) => tt(`token.${key}`);
  const [peerId, setPeerId] = useState(list.token?.peerId ?? '');
  const [revealed, setRevealed] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();
  const current = list.token ? peers.find((p) => p.id === list.token?.peerId)?.hostname ?? list.token.peerId : null;

  function issue() {
    startTransition(async () => {
      const res = await issueIngressTokenAction(peerId);
      if (res.ok) {
        setRevealed(res.token);
        onError(null);
      } else {
        onError(res.error);
      }
    });
  }

  return (
    <section className="space-y-3 rounded-2xl bg-ink-900 p-4">
      <h2 className="text-[15px] font-medium text-bamboo-50">{t('title')}</h2>
      <p className="text-[13px] text-bamboo-200/60">{t('description')}</p>
      <p className="text-xs text-bamboo-200/70">
        {list.token ? `${t('current')} ${current} · ${list.token.expiresAt}` : t('none')}
      </p>
      {peers.length === 0 ? (
        <p className="text-xs text-amber-400">{t('noIngressPeers')}</p>
      ) : (
        <div className="flex flex-wrap items-end gap-3">
          <Field id="ingress-token-peer" label={t('peerLabel')}>
            <select id="ingress-token-peer" value={peerId} onChange={(e) => setPeerId(e.target.value)} disabled={pending} className={inputCls}>
              <option value="">—</option>
              {peers.map((p) => <option key={p.id} value={p.id}>{p.hostname} ({p.ip})</option>)}
            </select>
          </Field>
          <button type="button" onClick={issue} disabled={pending || !peerId}
            className="rounded-full border border-bamboo-200/30 px-4 py-1.5 text-[13px] text-bamboo-50 hover:bg-[#777]/20">
            {list.token ? t('rotate') : t('issue')}
          </button>
        </div>
      )}
      {revealed && (
        <div className="space-y-2 rounded-lg border border-bamboo-300/40 p-3">
          <p className="text-xs font-medium text-bamboo-50">{t('revealTitle')}</p>
          <code className="block break-all font-mono text-xs text-bamboo-100">{revealed}</code>
          <p className="text-xs text-bamboo-200/60">{t('revealHint')}</p>
          <button type="button" onClick={() => setRevealed(null)} className="text-xs text-bamboo-200 underline">{t('close')}</button>
        </div>
      )}
    </section>
  );
}

const inputCls =
  'w-full rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1.5 font-mono text-xs text-bamboo-50 outline-none focus:border-bamboo-300';

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-xs text-bamboo-200/70">{label}</label>
      {children}
      {hint && <p className="text-[11px] text-bamboo-200/50">{hint}</p>}
    </div>
  );
}

function RowButton({ children, onClick, disabled }: { children: React.ReactNode; onClick: () => void; disabled: boolean }) {
  return (
    <button type="button" onClick={onClick} disabled={disabled}
      className="rounded-full border border-bamboo-200/30 px-3 py-0.5 text-xs text-bamboo-50 hover:bg-[#777]/20 disabled:opacity-50">
      {children}
    </button>
  );
}
```

Every sub-component takes `useTranslations('ingress')` and prefixes `form.` / `token.` itself, so the test's identity mock (which returns the key it is given) sees the full `form.submit`-style keys. The token panel uses one string per piece (`current`, then hostname and expiry) for the same reason.

Run: `npx vitest run src/components/IngressView.test.tsx` → 4 tests PASS.

- [ ] **Step 6: Page, settings card, translations**

`apps/web/src/app/[locale]/ingress/page.tsx`:

```tsx
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { FetchErrorState } from '@/components/FetchErrorState';
import { IngressView } from '@/components/IngressView';
import { fetchIngress, fetchMe, fetchPeers } from '@/lib/api';
import type { IngressList } from '@/lib/ingress';
import type { Peer } from '@/lib/types';

// /ingress: public services (docs/design/2026-11-02-public-services-ingress.md §7).
// Follows the relays page: server component fetches, FetchErrorState
// renders 401/403/5xx, a client view handles mutations.
export default async function IngressPage() {
  const [list, peers, me] = await Promise.all([fetchIngress(), fetchPeers(), fetchMe()]);
  if (list.kind !== 'ok') {
    return <FetchErrorState kind={list.kind} />;
  }
  const peerList = peers.kind === 'ok' ? peers.value.peers : [];
  return <IngressPageView list={list.value} peers={peerList} isAdmin={me.isAdmin === true} />;
}

function IngressPageView({ list, peers, isAdmin }: { list: IngressList; peers: Peer[]; isAdmin: boolean }) {
  const t = useTranslations('ingress');
  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">{t('title')}</h1>
        <p className="text-[13px] text-bamboo-200/60">{t('subtitle')}</p>
      </header>
      <IngressView list={list} peers={peers} isAdmin={isAdmin} />
    </div>
  );
}
```

In `settings/page.tsx`, render `{isAdmin && <IngressCard />}` right after `{isAdmin && <RelaysCard />}`, and add next to `RelaysCard`:

```tsx
function IngressCard() {
 const t = useTranslations('settings.ingressCard');
 return (
 <Link
 href="/ingress"
 className="group flex h-full items-start justify-between gap-4 rounded-2xl bg-ink-900 p-5 transition-colors hover:bg-ink-850"
 >
 <div className="min-w-0">
 <h3 className="text-[15px] font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1.5 text-[13px] leading-5 text-bamboo-200/60">{t('description')}</p>
 <p className="mt-3 text-xs font-medium text-bamboo-200/80 group-hover:text-bamboo-50">
 {t('link')} →
 </p>
 </div>
 </Link>
 );
}
```

`messages/en.json` — add a top-level `"ingress"` object (after `"relays"`) and `"ingressCard"` inside `"settings"` (after `"relaysCard"`):

```json
  "ingress": {
    "title": "Public services",
    "subtitle": "Publish a service running on a peer at https://<label>.<suffix>. Each service needs admin approval; the ingress peer forwards requests to it over the mesh.",
    "empty": "No public services yet.",
    "errorPrefix": "Error:",
    "generatedRule": "Generated ACL rule, not editable in the policy editor",
    "columns": { "label": "Label", "url": "URL", "peer": "Peer", "port": "Port", "state": "State", "actions": "Actions" },
    "state": { "pending": "Pending approval", "approved": "Approved, disabled", "live": "Live", "peerPending": "Peer not approved" },
    "actions": { "approve": "Approve", "enable": "Enable", "disable": "Disable", "delete": "Delete" },
    "form": {
      "openButton": "Add service",
      "cancel": "Cancel",
      "peerLabel": "Peer",
      "labelLabel": "Label",
      "labelHint": "Lowercase letters, digits and inner hyphens, up to 32 characters.",
      "portLabel": "Target port",
      "portHint": "1024–65535 on the peer's mesh IP.",
      "submit": "Create",
      "invalidLabel": "Use 1–32 lowercase letters, digits or inner hyphens.",
      "reservedLabel": "This label is reserved.",
      "invalidPort": "Port must be between 1024 and 65535.",
      "peerRequired": "Choose a peer."
    },
    "token": {
      "title": "Ingress service token",
      "description": "The ingress peer uses this token to fetch routes and send request logs. Issuing a new token revokes the old one.",
      "peerLabel": "Ingress peer (tagged ingress)",
      "issue": "Issue token",
      "rotate": "Rotate token",
      "current": "Current token:",
      "none": "No token issued.",
      "noIngressPeers": "No approved peer has the ingress tag.",
      "revealTitle": "Copy this token now",
      "revealHint": "Save it on the VPS as /etc/bamboo-ingress/ingress.token. It is not shown again.",
      "close": "Done"
    }
  },
```

```json
    "ingressCard": {
      "title": "Public services",
      "description": "Publish peer services on the internet through the ingress peer, with admin approval.",
      "link": "Go to Public services"
    },
```

`messages/zh-TW.json` — same keys:

```json
  "ingress": {
    "title": "公開服務",
    "subtitle": "把節點上的服務發布在 https://<label>.<suffix>。每個服務都要管理員核准，由入口節點經 mesh 轉送請求。",
    "empty": "尚無公開服務。",
    "errorPrefix": "錯誤：",
    "generatedRule": "系統產生的 ACL 規則，不能在政策編輯器修改",
    "columns": { "label": "名稱", "url": "網址", "peer": "節點", "port": "埠號", "state": "狀態", "actions": "操作" },
    "state": { "pending": "待核准", "approved": "已核准，未啟用", "live": "上線中", "peerPending": "節點未核准" },
    "actions": { "approve": "核准", "enable": "啟用", "disable": "停用", "delete": "刪除" },
    "form": {
      "openButton": "新增服務",
      "cancel": "取消",
      "peerLabel": "節點",
      "labelLabel": "名稱",
      "labelHint": "小寫英文字母、數字與中間的連字號，最多 32 個字元。",
      "portLabel": "目標埠號",
      "portHint": "節點 mesh IP 上的 1024–65535。",
      "submit": "建立",
      "invalidLabel": "請使用 1–32 個小寫英文字母、數字或中間的連字號。",
      "reservedLabel": "這個名稱是保留字。",
      "invalidPort": "埠號必須介於 1024 與 65535。",
      "peerRequired": "請選擇節點。"
    },
    "token": {
      "title": "入口服務權杖",
      "description": "入口節點用這個權杖取得路由並回傳請求紀錄。發新權杖會讓舊權杖失效。",
      "peerLabel": "入口節點（帶 ingress 標籤）",
      "issue": "發放權杖",
      "rotate": "更換權杖",
      "current": "目前權杖：",
      "none": "尚未發放權杖。",
      "noIngressPeers": "沒有帶 ingress 標籤的已核准節點。",
      "revealTitle": "現在複製這個權杖",
      "revealHint": "存到 VPS 的 /etc/bamboo-ingress/ingress.token。之後不會再顯示。",
      "close": "完成"
    }
  },
```

```json
    "ingressCard": {
      "title": "公開服務",
      "description": "經入口節點把節點上的服務發布到網際網路，需管理員核准。",
      "link": "前往公開服務"
    },
```

- [ ] **Step 7: Run the web gate (same as CI)**

Run:
```bash
cd /Users/hanfourhuang/Projects/bamboo/apps/web
npm run lint && npm run typecheck && npm run test && npm run build
```
Expected: lint `✔ No ESLint warnings or errors`; typecheck exits 0; vitest reports all files passed including `ingress.test.ts` and `IngressView.test.tsx`; `next build` lists `ƒ /[locale]/ingress`.

- [ ] **Step 8: Commit**

```bash
cd /Users/hanfourhuang/Projects/bamboo
git add apps/web/src/lib/ingress.ts apps/web/src/lib/ingress.test.ts apps/web/src/lib/ingressActions.ts apps/web/src/lib/api.ts apps/web/src/components/IngressView.tsx apps/web/src/components/IngressView.test.tsx "apps/web/src/app/[locale]/ingress/page.tsx" "apps/web/src/app/[locale]/settings/page.tsx" apps/web/messages/en.json apps/web/messages/zh-TW.json
git commit -m "feat(web): public services page with approval, enable, delete, and token issue

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: `infra/full` wiring and deployment docs

**Files:**
- Modify: `infra/full/Caddyfile`
- Modify: `infra/full/docker-compose.yml` (controller env + loopback port; caddy env + `extra_hosts`; new `ingress` service under profile `ingress`)
- Modify: `infra/full/.env.example`
- Modify: `infra/full/deploy.sh` (pull/recreate `ingress` only when the profile is enabled)
- Create: `docs/deployment/public-services-ingress.md`

**Interfaces:**
- Consumes: image `ghcr.io/hanfour/bamboo-ingress` (Task 8); controller env `BAMBOO_INGRESS_SIGNING_KEY`, `BAMBOO_INGRESS_SUFFIX` (Task 3); ingress env from Task 8; `/internal/v1/ingress/ask` (Task 4).
- Produces: operator-facing env `INGRESS_SUFFIX`, `BAMBOO_INGRESS_SIGNING_KEY`, `COMPOSE_PROFILES=ingress`; token file path `/etc/bamboo-ingress/ingress.token` on the host.

Safety properties this task must keep (verify each in Step 5):
1. With `INGRESS_SUFFIX` unset, Caddy still validates (the site falls back to `*.svc.invalid`, which no DNS points at and `ask` refuses).
2. With `COMPOSE_PROFILES` unset, `docker compose up -d` and `deploy.sh` start exactly the services they start today.
3. The control-plane and relay site blocks route exactly as before; the only change there is an explicit `/internal/*` → 404 guard on the control-plane site (today that path falls through to the web UI, which already 404s).

- [ ] **Step 1: Caddyfile**

Add a global options block at the very top of `infra/full/Caddyfile` (global options must come first), add the guard inside the `{$DOMAIN}` block as its first `handle`, and append the new site:

```caddyfile
# Global options. on_demand_tls only affects sites that opt in with
# `tls { on_demand }` — today just the public-services site below. The
# ask URL is the controller's REST listener inside the compose network.
{
    on_demand_tls {
        ask http://controller:8081/internal/v1/ingress/ask
    }
}
```

Inside `{$DOMAIN} { … }`, before `@controller`:

```caddyfile
    # Ingress ↔ controller endpoints are internal only (public services
    # ingress spec §4.3). Never forward them from a public site.
    @internal path /internal/*
    handle @internal {
        respond 404
    }
```

At the end of the file:

```caddyfile
# Public services (docs/design/2026-11-02-public-services-ingress.md).
# One certificate per name, issued on demand only after the controller's
# ask endpoint confirms the name is a live service. No wildcard cert.
# bamboo-ingress runs on the host network and listens on the docker
# bridge gateway (host.docker.internal → host-gateway).
# INGRESS_SUFFIX unset ⇒ *.svc.invalid, which nothing resolves to.
*.{$INGRESS_SUFFIX:svc.invalid} {
    tls {
        on_demand
    }
    @internal path /internal/*
    handle @internal {
        respond 404
    }
    handle {
        reverse_proxy host.docker.internal:8090 {
            flush_interval -1
        }
    }
}
```

- [ ] **Step 2: docker-compose.yml**

`controller` service — add to `environment`:

```yaml
      # Public services ingress. Empty key ⇒ token/routes endpoints answer
      # 503 and nothing else changes. Generate with `controller relaykey`
      # and use the SIGNING value (must differ from the relay key).
      BAMBOO_INGRESS_SIGNING_KEY: "${BAMBOO_INGRESS_SIGNING_KEY:-}"
      BAMBOO_INGRESS_SUFFIX: "${INGRESS_SUFFIX:-}"
```

and a loopback-only port so the host-network ingress can reach `/internal/*` (not reachable from outside the host):

```yaml
    ports:
      - "127.0.0.1:8081:8081"
```

`caddy` service — add to `environment` and a new `extra_hosts`:

```yaml
      INGRESS_SUFFIX: "${INGRESS_SUFFIX:-}"
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

New service (after `ai`):

```yaml
  # Public services ingress proxy. Opt-in: set COMPOSE_PROFILES=ingress in
  # .env once the token file exists (docs/deployment/public-services-ingress.md).
  # Host network so it reaches peer mesh IPs through the host's bamboo0
  # WireGuard interface; it listens on the docker bridge gateway so only
  # containers (Caddy) and the host reach it.
  ingress:
    image: ghcr.io/hanfour/bamboo-ingress:${BAMBOO_IMAGE_TAG:-latest}
    profiles: ["ingress"]
    restart: unless-stopped
    network_mode: host
    environment:
      BAMBOO_INGRESS_SUFFIX: "${INGRESS_SUFFIX:-}"
      BAMBOO_INGRESS_LISTEN: "${INGRESS_LISTEN:-172.17.0.1:8090}"
      BAMBOO_INGRESS_ADMIN_LISTEN: "127.0.0.1:8091"
      BAMBOO_CONTROLLER_URL: "http://127.0.0.1:8081"
      BAMBOO_INGRESS_TOKEN_FILE: "/run/bamboo/ingress.token"
    volumes:
      # Owned by uid 65532 (distroless nonroot), mode 0400.
      - /etc/bamboo-ingress/ingress.token:/run/bamboo/ingress.token:ro
    depends_on:
      controller:
        condition: service_healthy
```

- [ ] **Step 3: .env.example and deploy.sh**

Append to `infra/full/.env.example`:

```bash
# Public services ingress (docs/deployment/public-services-ingress.md).
# Leave all three empty/commented to keep the feature off.
# INGRESS_SUFFIX=svc.miilink.net
# Ed25519 seed; generate with `docker compose run --rm controller relaykey`
# and paste the SIGNING value. Must differ from BAMBOO_RELAY_SIGNING_KEY.
# BAMBOO_INGRESS_SIGNING_KEY=
# Start the ingress container (only after /etc/bamboo-ingress/ingress.token exists):
# COMPOSE_PROFILES=ingress
```

In `deploy.sh`, after `docker compose up -d controller web relay` (step 4), add:

```bash
# Public services ingress is opt-in (COMPOSE_PROFILES=ingress in .env).
if grep -Eq '^COMPOSE_PROFILES=.*\bingress\b' .env; then
    log "pulling + recreating ingress"
    docker compose pull ingress && docker compose up -d ingress \
        || echo "warning: ingress did not start; the control plane is unaffected"
fi
```

The existing pull step stays as it is; the ingress image is pulled only inside this guarded block.

- [ ] **Step 4: Deployment doc**

Create `docs/deployment/public-services-ingress.md`:

```markdown
# Public services ingress — operator guide

Design: `docs/design/2026-11-02-public-services-ingress.md`. This page is
the single-VPS (`infra/full`) runbook.

## What runs where

- DNS `*.svc.<domain>` → the VPS's static IP.
- Caddy (existing container) terminates TLS. Certificates are issued per
  name, on demand, only when `GET http://controller:8081/internal/v1/ingress/ask?domain=<name>` answers 200.
- `bamboo-ingress` (container `ingress`, host network) listens on the
  docker bridge gateway `172.17.0.1:8090`, polls the controller every 10 s
  over `127.0.0.1:8081`, and proxies to `<peer mesh IP>:<port>` over the
  host's WireGuard interface (`bamboo0`). Admin listener: `127.0.0.1:8091`
  (`/healthz`, `/version`, `/metrics`).
- The VPS is an ordinary mesh peer (`bamboo up` + `bamboo install-service`)
  tagged `ingress`, in the same tenant as the services it serves.

## Enable

1. DNS: add `*.svc.<domain>  A  <static IP>`. Check: `dig +short x.svc.<domain>`.
2. Key: `docker compose run --rm controller relaykey`; put the SIGNING
   value in `.env` as `BAMBOO_INGRESS_SIGNING_KEY=`; set `INGRESS_SUFFIX=svc.<domain>`.
3. Deploy: `./deploy.sh` (restarts the controller with the new env and
   reloads Caddy).
4. Ingress peer: if the VPS already runs a bamboo peer (`systemctl status bamboo`,
   `sudo wg show bamboo0`), reuse it; otherwise mint a pre-auth key and run
   `sudo env BAMBOO_AUTH_KEY=<key> bamboo install-service --server https://<domain> --hostname vps-ingress`
   (writes its env file and starts the systemd unit). Tag the peer `ingress` on the Peers page.
5. Token: Settings → Public services → Issue token for the VPS peer. Save it:
   `sudo install -d -m 0755 /etc/bamboo-ingress && sudo sh -c 'cat > /etc/bamboo-ingress/ingress.token' && sudo chown 65532:65532 /etc/bamboo-ingress/ingress.token && sudo chmod 0400 /etc/bamboo-ingress/ingress.token`
   (paste, then Ctrl-D).
6. Start: add `COMPOSE_PROFILES=ingress` to `.env`, then `docker compose up -d ingress`.
7. Check: `curl -s 127.0.0.1:8091/healthz` → `ok`; `curl -s 127.0.0.1:8091/metrics | grep bamboo_ingress_routes`.

## The target service

- It must listen on the peer's mesh IP at the configured port (1024–65535);
  a service bound to `127.0.0.1` is not reachable. Restrict its source to the
  ingress peer's mesh IP: the ACL is L3 only.
- It receives `X-Forwarded-For`, `X-Forwarded-Proto: https`, `X-Forwarded-Host`,
  `X-Bamboo-Ingress-Service`, and the public `Host`. Trust them only on
  connections from the ingress peer's mesh IP.
- Limits: 120 req/min per service, 300 req/min per client IP, 8 MiB body,
  10 min idle stream. Authentication (bearer/OAuth) is the service's job.

## Disable / roll back

- One service: disable or delete it in the UI (routes drop within 10 s,
  open streams are cut).
- Whole feature: remove `COMPOSE_PROFILES=ingress`, `docker compose stop ingress && docker compose rm -f ingress`.
  Leaving the Caddy site in place is harmless: `ask` refuses every name.
- Migration 00024 is additive; roll back only with the rest of the release
  (`docker compose run --rm controller migrate down` once).
```

- [ ] **Step 5: Verify**

Run:
```bash
cd /Users/hanfourhuang/Projects/bamboo/infra/full
cp .env.example /tmp/ingress-env-check.env
docker compose --env-file /tmp/ingress-env-check.env config -q && echo compose-ok
docker compose --env-file /tmp/ingress-env-check.env config --services
COMPOSE_PROFILES=ingress docker compose --env-file /tmp/ingress-env-check.env config --services | grep -x ingress
for suffix in "" "svc.example.test"; do
  docker run --rm -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" \
    -e DOMAIN=bamboo.example.test -e RELAY_DOMAIN=relay.example.test -e INGRESS_SUFFIX="$suffix" \
    caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1 | tail -1
done
bash -n deploy.sh && echo deploy-syntax-ok
rm /tmp/ingress-env-check.env
```
Expected: `compose-ok`; the default service list is `postgres clickhouse controller web relay caddy` in some order (no `ingress`, no `ai`); the profile run prints `ingress`; both Caddy runs end with `Valid configuration`; `deploy-syntax-ok`.

- [ ] **Step 6: Commit**

```bash
cd /Users/hanfourhuang/Projects/bamboo
git add infra/full/Caddyfile infra/full/docker-compose.yml infra/full/.env.example infra/full/deploy.sh docs/deployment/public-services-ingress.md
git commit -m "feat(infra): opt-in public services ingress for the single-VPS stack

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Live acceptance on the Tokyo VPS (manual)

> **STOP — explicit user approval required before Step 2.** Steps 2–12 change production (`bamboo.miilink.net`, Lightsail Tokyo): DNS, `.env`, a controller restart, a Caddy reload, a new container. Do not run them on delegated authority. Present the merged commit, the image tag, and this task's checklist, and wait for a "go" from the user. Step 1 is read-only and may run first.

**Files:** none in the repo. Record results in the PR description (table: step → expected → observed).

**Interfaces:**
- Consumes: everything above, merged to `main` and published as images `ghcr.io/hanfour/bamboo-{controller,web,relay,ingress}:<sha7>` by `images.yml`.
- Produces: evidence for spec §8 row 6.

- [ ] **Step 1: Read-only pre-checks (no approval needed)**

On the VPS (`ssh` as the deploy user, `cd <repo>/infra/full`):
```bash
git -C ../.. log --oneline -1
docker compose ps --format '{{.Service}} {{.Status}}'
curl -s -o /dev/null -w '%{http_code}\n' https://bamboo.miilink.net/healthz
systemctl is-active bamboo; sudo wg show bamboo0 | head -5
ip -4 addr show docker0 | grep inet
sudo ufw status 2>/dev/null | head -3
df -h / | tail -1
```
Expected: controller/web/relay/caddy `Up`; healthz `200`; note whether a bamboo peer already runs on `bamboo0` (reuse it as the ingress peer if so); `docker0` is `172.17.0.1/16` (otherwise set `INGRESS_LISTEN=<docker0 IP>:8090` in `.env`); ufw inactive or allowing docker bridges → host `8090`; ≥ 2 GB free.

- [ ] **Step 2: STOP — get approval.** Proceed only after the user says go.

- [ ] **Step 3: DNS**

Create `*.svc.miilink.net  A  <Lightsail static IP>` at the DNS provider. Check from a laptop: `dig +short probe.svc.miilink.net` → the static IP.

- [ ] **Step 4: Configure and deploy**

```bash
docker compose run --rm controller relaykey        # copy the SIGNING value
# edit .env: INGRESS_SUFFIX=svc.miilink.net and BAMBOO_INGRESS_SIGNING_KEY=<value>
./deploy.sh <sha7>
```
Expected: deploy ends with `OK — controller healthy` and `deploy complete`; `migrate status` shows `00024_ingress_services.sql` applied.

- [ ] **Step 5: Prove nothing else moved**

```bash
curl -s https://bamboo.miilink.net/healthz                                        # ok
curl -s -o /dev/null -w '%{http_code}\n' https://bamboo.miilink.net/              # 200 or 307 as before
curl -s -o /dev/null -w '%{http_code}\n' "https://bamboo.miilink.net/internal/v1/ingress/ask?domain=x.svc.miilink.net"  # 404
curl -s -o /dev/null -w '%{http_code}\n' https://<RELAY_DOMAIN>/healthz           # 200
curl -s "http://127.0.0.1:8081/internal/v1/ingress/ask?domain=nope.svc.miilink.net" -o /dev/null -w '%{http_code}\n'  # 404
```
Also: an existing Mac peer still connects (bamboo.app shows connected; `ping <another peer mesh IP>` works).

- [ ] **Step 6: Ingress peer and token**

Reuse the VPS peer from Step 1, or install one (`docs/deployment/public-services-ingress.md` step 4). In the web UI: Peers → VPS peer → tags `ingress`. Settings → Public services → Issue token for the VPS peer; save it to `/etc/bamboo-ingress/ingress.token` (owner 65532, mode 0400) per the doc.

- [ ] **Step 7: Target service on the Mac**

YesChef listens on `127.0.0.1:47800`, which the mesh cannot reach (see "Spec gaps" below). For this acceptance run, forward from the Mac's mesh IP, accepting only the ingress peer:
```bash
# on the Mac; MESH_IP = Mac's bamboo IP, INGRESS_IP = VPS peer's mesh IP
socat TCP-LISTEN:47801,bind=$MESH_IP,fork,reuseaddr,range=$INGRESS_IP/32 TCP:127.0.0.1:47800
```
Check from the VPS: `curl -s -o /dev/null -w '%{http_code}\n' http://$MESH_IP:47801/` returns YesChef's status (not `000`). If it is `000`, check the ACL (Step 8 enables it) and the macOS application firewall.

- [ ] **Step 8: Create, approve, enable**

UI: Public services → Add service: peer = the Mac, label `yeschef`, port `47801` → Approve → Enable. The row shows `Live` and `tag:ingress -> <Mac IP>:47801`.

- [ ] **Step 9: Start the ingress**

```bash
grep -q '^COMPOSE_PROFILES=' .env || echo 'COMPOSE_PROFILES=ingress' >> .env
docker compose up -d ingress
sleep 12
docker compose logs --tail=5 ingress
curl -s 127.0.0.1:8091/healthz; echo
curl -s 127.0.0.1:8091/metrics | grep -E '^bamboo_ingress_routes '
docker compose exec caddy wget -qO- -S --header 'Host: yeschef.svc.miilink.net' http://host.docker.internal:8090/ 2>&1 | head -1
```
Expected: logs show `bamboo-ingress listening`; healthz `ok`; `bamboo_ingress_routes 1`; the Caddy → ingress probe returns YesChef's HTTP status line.

- [ ] **Step 10: From the internet (a machine that is not a mesh member)**

```bash
curl -sv https://yeschef.svc.miilink.net/ 2>&1 | grep -E 'subject:|issuer:|HTTP/'
curl -sN -X POST https://yeschef.svc.miilink.net/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H "Authorization: Bearer <YesChef MCP token>" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
curl -sv https://nope-$(date +%s).svc.miilink.net/ 2>&1 | grep -iE 'alert|error' | head -2
```
Expected: certificate `subject: CN=yeschef.svc.miilink.net`, issuer Let's Encrypt; the MCP `initialize` returns a JSON-RPC result (SSE `data:` lines arrive as they are sent); the unknown name fails the TLS handshake (ask 404 → no certificate). ClickHouse: `docker compose exec clickhouse clickhouse-client --user bamboo --password "$CLICKHOUSE_PASSWORD" -q "SELECT http_status, client_ip, service_id FROM bamboo.connection_events WHERE event_type='ingress_request' ORDER BY occurred_at DESC LIMIT 3"` shows the requests.

- [ ] **Step 11: Disable within 10 s**

From the internet machine, start:
```bash
for i in $(seq 1 20); do printf '%s ' "$(date +%T)"; curl -s -o /dev/null -w '%{http_code}\n' https://yeschef.svc.miilink.net/; sleep 1; done
```
and while it runs, click Disable in the UI and note the time. Expected: the code changes from YesChef's status to `404` no more than 10 s after the click (route poll interval); an open `curl -N` SSE stream started before the click ends at the same moment.

- [ ] **Step 12: Leave it in the agreed state**

Ask the user whether to re-enable `yeschef` or roll back (doc "Disable / roll back"). Stop the `socat` forwarder if YesChef will bind the mesh IP itself.

---

## Spec gaps and risks found while planning

1. **Target must listen on the mesh IP.** YesChef binds `127.0.0.1:47800`; the spec routes to `<peer mesh IP>:<target_port>` and has no peer-side forwarder. Task 11 uses `socat` as a stopgap. A real fix (YesChef binding the mesh IP with a source check, or a bamboo.app forwarder) is outside this spec.
2. **Telemetry transport** is not specified; the ingress cannot reach ClickHouse. Decision 6 routes it through a new internal endpoint and four additive ClickHouse columns. `bamboo-ai` trains on all `connection_events` rows, so `ingress_request` rows will enter its features; review whether to filter them out there.
3. **Rate-limiter reuse** is impossible across modules (Go `internal/`); the plan copies 110 lines (Decision 9).
4. **Controller reachability** from a host-network container needs `127.0.0.1:8081` published (Decision 7) — one line on an existing service, which the spec's "only adds a site block, a compose service, and a systemd unit" did not foresee.
5. **"Only Caddy can reach :8090"** is not quite true: every container and host process can reach the docker bridge gateway. Acceptable on a single-tenant VPS; noted in the doc.
6. **Return-path ACL rule.** WireGuard needs the ingress IP in the target's AllowedIPs, so the plan generates a second rule (Decision 8). Without it the spec's single rule would not carry traffic.
7. **Tenants with no ACL rules** are full mesh; generated rules are skipped there so the tenant is not switched to default-deny (Decision 8).
8. **Token rotation** needs server-side state the spec does not list (`ingress_tokens`, Decision 4); token file moved to `/etc/bamboo-ingress/` because `/etc/bamboo` is the CLI's 0700 env directory.
9. **Host header.** The proxy preserves the public `Host`. MCP servers with DNS-rebinding protection may reject `yeschef.svc.miilink.net` until it is allow-listed on the service side.
10. **Let's Encrypt quota** (50/week for `miilink.net`) is shared with the control plane; on-demand issuance is gated by `ask`, so only approved names consume it.
11. **10-second disable** relies on the 10 s poll; worst case is ~10 s plus one request round-trip. The plan cancels in-flight streams on route removal to make it hold for SSE.
