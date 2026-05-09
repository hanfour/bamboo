# Sprint 1 + 2 Tickets (Weeks 1–4)

This document defines the discrete units of work for the first two sprints.
Each ticket is scoped to be importable into GitHub Issues with minimal edits.

## Conventions

- **Estimate**: S (≤1 day), M (1–3 days), L (3–7 days), XL (>1 sprint, split it)
- **Labels**: `area:*` for component, `type:*` for kind, `sprint-1` / `sprint-2`
- **Acceptance criteria**: bullet list — must all be true to close.

## Milestones

- **Sprint 1** (Weeks 1–2): infrastructure + first connection proof
- **Sprint 2** (Weeks 3–4): authentication + multi-platform clients

---

# Sprint 1 — Infrastructure + Connection Proof

## #1 — Bootstrap monorepo Go workspaces

- **Labels**: `area:infra`, `type:setup`, `sprint-1`
- **Estimate**: S
- **Owner**: Tech Lead

### Description
Initialize Go workspaces (`go.work`) so all Go modules under `apps/` and
`clients/` build and test together. Each module gets its own `go.mod`.

### Acceptance criteria
- [ ] `go.work` file at repo root listing all Go modules
- [ ] `apps/controller/go.mod` initialized
- [ ] `apps/relay/go.mod` initialized
- [ ] `clients/core/go.mod` initialized (will become AGPL after NetBird import)
- [ ] `clients/cli/go.mod` initialized
- [ ] `make bootstrap` installs Go, protoc, buf, golangci-lint
- [ ] `go build ./...` succeeds (even if all stubs)

---

## #2 — NetBird fork: sandbox clone and inventory

- **Labels**: `area:infra`, `type:setup`, `sprint-1`
- **Estimate**: M
- **Owner**: Backend
- **Blocks**: #3, #4, #6

### Description
Follow [NetBird Fork SOP](./netbird-fork-sop.md) steps 1–3. Create the
read-only sandbox clone, working clone, and inventory of which directories
to keep, strip, or replace.

### Acceptance criteria
- [ ] `~/scratch/bamboo-fork/netbird-upstream` exists as untouched reference
- [ ] `~/scratch/bamboo-fork/netbird-working` exists with `upstream` remote
- [ ] `UPSTREAM_SHA` recorded
- [ ] Inventory document checked in at `docs/development/netbird-import-inventory.md`
- [ ] License-impact review approved by tech lead

---

## #3 — Import NetBird management → apps/controller

- **Labels**: `area:controller`, `type:import`, `sprint-1`
- **Estimate**: L
- **Blocked by**: #2

### Description
Per Fork SOP step 4–6, copy NetBird's `management/` into
`apps/controller/internal/`, rewrite import paths, drop in the AGPLv3
LICENSE notice, create the `ORIGIN` file.

### Acceptance criteria
- [ ] `apps/controller/internal/` populated from upstream
- [ ] `apps/controller/ORIGIN` records upstream commit SHA + import date
- [ ] `apps/controller/LICENSE` contains AGPLv3 text
- [ ] All imports rewritten to `github.com/hanfour/bamboo/apps/controller/...`
- [ ] `go build ./apps/controller/...` succeeds
- [ ] `go test ./apps/controller/...` runs (failures allowed but recorded)

---

## #4 — Import NetBird relay → apps/relay

- **Labels**: `area:relay`, `type:import`, `sprint-1`
- **Estimate**: M
- **Blocked by**: #2

### Description
Same procedure as #3 for the relay server.

### Acceptance criteria
- [ ] `apps/relay/internal/` populated from upstream
- [ ] `apps/relay/ORIGIN` records SHA
- [ ] `apps/relay/LICENSE` AGPLv3
- [ ] `go build ./apps/relay/...` succeeds

---

## #5 — Database schema v1 (multi-tenant)

- **Labels**: `area:controller`, `type:design`, `sprint-1`
- **Estimate**: M

### Description
Design the initial Postgres schema. Multi-tenant from day one — every
tenant-scoped table has `tenant_id`. Migrations live in
`apps/controller/migrations/`.

### Acceptance criteria
- [ ] Migration tool chosen and added to `make bootstrap` (recommend `goose` or `golang-migrate`)
- [ ] Tables: `tenants`, `users`, `groups`, `peers`, `tags`, `acl_policies`,
      `pre_auth_keys`, `audit_log`
- [ ] Every tenant-scoped table has `tenant_id` with FK + index
- [ ] `migrations/0001_init.sql` checked in
- [ ] Migration runs cleanly on a fresh Postgres 16 from `infra/docker-compose.yml`

---

## #6 — gRPC proto first cut

- **Labels**: `area:proto`, `type:design`, `sprint-1`
- **Estimate**: M

### Description
Define the first version of gRPC protos in `proto/bamboo/v1/`. Cover auth,
coordinator, policy, telemetry. See I-deliverable in this repo.

### Acceptance criteria
- [ ] `proto/buf.yaml` and `proto/buf.gen.yaml` configured
- [ ] `proto/bamboo/v1/auth.proto` defined
- [ ] `proto/bamboo/v1/coordinator.proto` defined
- [ ] `proto/bamboo/v1/policy.proto` defined
- [ ] `proto/bamboo/v1/telemetry.proto` defined
- [ ] `make proto` regenerates Go code into `apps/controller/gen/` and `clients/core/gen/`
- [ ] `buf lint` passes

---

## #7 — CI pipeline first cut

- **Labels**: `area:infra`, `type:ci`, `sprint-1`
- **Estimate**: M
- **Owner**: SRE

### Description
Replace the placeholder CI workflow with real lint, test, build steps.

### Acceptance criteria
- [ ] `make lint` runs `golangci-lint`, `eslint`, `ruff` (each on its scope)
- [ ] `make test` runs Go tests, web tests, Python tests
- [ ] CI matrix: Go 1.23 on ubuntu-latest + macos-latest
- [ ] Caching for Go modules, npm, and pip
- [ ] License-check job verifies SPDX headers on every Go file
- [ ] Branch protection on `main`: require CI green + 1 approval

---

## #8 — Staging environment baseline

- **Labels**: `area:infra`, `type:deploy`, `sprint-1`
- **Estimate**: L
- **Owner**: SRE

### Description
Create the first staging environment on AWS Tokyo. Minimal: VPC, single-AZ
EKS, RDS Postgres single instance, ElastiCache single node.

### Acceptance criteria
- [ ] `infra/terraform/environments/staging/` Terraform code applied successfully
- [ ] EKS cluster reachable via kubectl from local machine
- [ ] Postgres reachable from within cluster (private subnet)
- [ ] Redis reachable from within cluster
- [ ] DNS subdomain `staging.<domain>` pointed at NLB
- [ ] Terraform state in S3 backend with state locking
- [ ] All secrets in AWS Secrets Manager, not Terraform vars

---

## #9 — Local dev stack via docker-compose

- **Labels**: `area:infra`, `type:dx`, `sprint-1`
- **Estimate**: S

### Description
Wire up `infra/docker-compose.yml` so a developer can run `make dev` and
get Postgres + Redis + (later) controller running locally.

### Acceptance criteria
- [ ] `make dev` starts stack
- [ ] Postgres on `localhost:5432` with credentials in `.env.example`
- [ ] Redis on `localhost:6379`
- [ ] `make dev-down` stops cleanly
- [ ] README in `infra/` documents usage

---

## #10 — NAT traversal test harness

- **Labels**: `area:client`, `type:test`, `sprint-1`
- **Estimate**: L
- **Owner**: QA

### Description
Build automated test harness that simulates NAT scenarios using Linux
network namespaces. Must cover: no-NAT, full-cone, restricted-cone,
port-restricted-cone, symmetric.

### Acceptance criteria
- [ ] Script under `tools/nat-test/` creates 5 namespace topologies
- [ ] Two test peers can be launched, one in each topology
- [ ] CI job runs the harness against the WireGuard tunnel logic
- [ ] Results table reports success rate per scenario
- [ ] Symmetric NAT test must succeed via relay fallback

---

# Sprint 2 — Authentication + Multi-platform Clients

## #11 — OIDC provider scaffold (Google + GitHub)

- **Labels**: `area:controller`, `type:feature`, `sprint-2`
- **Estimate**: L

### Description
Add OIDC login support to the controller. First two providers: Google,
GitHub. Code organized so additional providers (Microsoft, Okta) can be
added with a config block.

### Acceptance criteria
- [ ] `OIDCProvider` interface in `apps/controller/internal/auth/`
- [ ] Google + GitHub implementations
- [ ] Login endpoint redirects to provider, handles callback
- [ ] Successful login creates `User` row, returns JWT session
- [ ] JWT validation middleware on protected endpoints
- [ ] Tests cover token signing, validation, expiry, replay

---

## #12 — PreAuthKey creation and redemption

- **Labels**: `area:controller`, `type:feature`, `sprint-2`
- **Estimate**: M

### Description
PreAuthKeys allow CI/CD systems and headless servers to register without
interactive login.

### Acceptance criteria
- [ ] CLI: `bamboo authkey create --tenant=X --tags=server,prod --expires=30d`
- [ ] Controller stores hashed key (never plaintext)
- [ ] Client can register with `--auth-key=...`
- [ ] Audit log records creation, redemption, revocation
- [ ] One-time vs reusable flag supported

---

## #13 — Peer registration handshake

- **Labels**: `area:controller`, `area:client`, `type:feature`, `sprint-2`
- **Estimate**: L

### Description
Implement the gRPC `Coordinator.Register` flow: client presents identity,
generates WireGuard key, controller assigns IP, returns peer set.

### Acceptance criteria
- [ ] `Register` RPC handler on controller
- [ ] Client implements registration call
- [ ] IP allocation from `100.64.0.0/10` per tenant
- [ ] WireGuard public key stored on controller
- [ ] Controller returns initial peer set in response
- [ ] Re-registration is idempotent

---

## #14 — Long-poll peer set updates

- **Labels**: `area:controller`, `area:client`, `type:feature`, `sprint-2`
- **Estimate**: L

### Description
Clients need updates when peers join/leave or ACL changes. Implement
gRPC server-streaming `WatchPeers` RPC.

### Acceptance criteria
- [ ] `Coordinator.WatchPeers` server-streaming RPC
- [ ] Controller publishes events on peer add/remove/update
- [ ] Client maintains stream, applies updates to WireGuard config
- [ ] Reconnection with exponential backoff on stream break
- [ ] Heartbeat every 30s; controller marks peer offline after 90s

---

## #15 — First end-to-end tunnel

- **Labels**: `area:client`, `type:feature`, `sprint-2`
- **Estimate**: XL → split if needed
- **Blocks**: #18

### Description
Two clients (both Linux) register with controller, exchange WireGuard
keys, establish a direct tunnel, and ping each other.

### Acceptance criteria
- [ ] Smoke test in `apps/controller/test/e2e/` runs in CI
- [ ] Test launches controller + 2 clients in containers
- [ ] After registration, `ping` from client A to client B succeeds
- [ ] Test asserts WireGuard interface up on both sides
- [ ] Test passes ≥ 99% in 100 consecutive runs

---

## #16 — macOS client app shell

- **Labels**: `area:client-macos`, `type:feature`, `sprint-2`
- **Estimate**: L

### Description
Minimal macOS app: menu bar icon, login button, status display. Wraps
`clients/core` Go binary via xcframework.

### Acceptance criteria
- [ ] Xcode project under `clients/apple/`
- [ ] App builds and signs (developer team ID configured)
- [ ] Menu bar shows connection status
- [ ] "Connect" button initiates OIDC login flow in browser
- [ ] System Extension entitlement requested for NetworkExtension

---

## #17 — Linux daemon and CLI

- **Labels**: `area:client-linux`, `type:feature`, `sprint-2`
- **Estimate**: M

### Description
`bamboo` CLI binary + systemd service for Linux.

### Acceptance criteria
- [ ] `bamboo up`, `bamboo down`, `bamboo status` commands work
- [ ] systemd unit file in `clients/linux/packaging/systemd/`
- [ ] DEB package builds via `goreleaser` config
- [ ] WireGuard kernel module preferred; userspace fallback documented

---

## #18 — Web UI scaffold

- **Labels**: `area:web`, `type:feature`, `sprint-2`
- **Estimate**: L

### Description
Next.js 14 app with login, peers list, basic ACL view. i18n configured
from day one (en, zh-TW).

### Acceptance criteria
- [ ] `apps/web/` Next.js project initialized
- [ ] Auth flow connects to controller OIDC
- [ ] Peers page lists tenant's peers (live data)
- [ ] ACL page shows current policy (read-only OK)
- [ ] i18n: every string goes through `t()`
- [ ] zh-TW locale 100% covered

---

## #19 — Audit log

- **Labels**: `area:controller`, `type:feature`, `sprint-2`
- **Estimate**: M

### Description
Every significant action writes to `audit_log` table. Web UI exposes a
read-only viewer (Phase 2 will expose API).

### Acceptance criteria
- [ ] Audit middleware on every mutation handler
- [ ] Records: actor, action, resource, before/after diff (where useful)
- [ ] Retention policy configurable (default 90 days)
- [ ] Test asserts no mutation goes unrecorded

---

## #20 — Sprint 2 retrospective + Phase 1 plan check

- **Labels**: `type:meta`, `sprint-2`
- **Estimate**: S

### Description
At end of Sprint 2, review velocity vs Phase 1 plan. Adjust Sprints 3–8.

### Acceptance criteria
- [ ] Velocity metrics for Sprints 1–2 documented
- [ ] Phase 1 → Phase 2 gate criteria reviewed
- [ ] Sprint 3 ticket list drafted
- [ ] Risk register updated

---

# Cross-cutting tracking

## Risks to surface in standup

- AGPL-Apache boundary discipline (any PR mixing the two)
- macOS NetworkExtension entitlement approval (Apple developer process is slow)
- Symmetric NAT success rate (#10 is the early signal)

## What goes to the next sprint

Tickets that don't close by sprint end roll into the next sprint. Do not
inflate scope mid-sprint.
