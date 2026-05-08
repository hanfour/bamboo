# NetBird Import Inventory

This document maps every top-level directory of the upstream NetBird repository
to a decision: **KEEP & ADAPT**, **STRIP**, or **REPLACE**. It is the operating
plan for [Issue #2 — NetBird fork: sandbox clone and inventory](https://github.com/hanfour/bamboo/issues/2).

> **Source baseline**: NetBird default branch at the SHA recorded in
> `~/scratch/bamboo-fork/UPSTREAM_SHA` after running `scripts/netbird-prep.sh`.
> This inventory was last reconciled against upstream SHA
> [`7da94a4956af76f7187733aa488e9d20a0f62202`](https://github.com/netbirdio/netbird/commit/7da94a4956af76f7187733aa488e9d20a0f62202).
> Re-run `scripts/netbird-audit.sh` before any import to detect drift.

## Legend

| Mark | Meaning |
| ---- | ------- |
| **KEEP** | Import as-is (with import-path rewrite). Preserve AGPL license. |
| **ADAPT** | Import then modify. Track diffs against upstream. |
| **STRIP** | Do not import. We have our own equivalent or do not need this. |
| **REPLACE** | Do not import. Build our own from public specs (clean-room). |
| **REFERENCE** | Do not import. Read for understanding only; rewrite from scratch. |

---

## Top-level directories

| Path                     | Decision   | Target in bamboo                              | License (target) | Notes |
| ------------------------ | ---------- | --------------------------------------------- | ---------------- | ----- |
| `client/`                | ADAPT      | `clients/core/internal/`                      | AGPLv3 (interim) | Heavy. Plan clean-room rewrite per [ADR 0011](../adr/0011-client-core-relicensing-path.md). |
| `client/cmd/`            | STRIP      | —                                             | —                | We author our own CLI in `clients/cli/`. |
| `client/ui/`             | STRIP      | —                                             | —                | We author our own native clients (macOS/Windows/Linux). |
| `client/internal/`       | KEEP       | `clients/core/internal/`                      | AGPLv3 (interim) | Connection state machine, peer mgr — substantial code. |
| `management/`            | KEEP       | `apps/controller/internal/`                   | AGPLv3           | Control plane core. |
| `management/server/`     | KEEP       | `apps/controller/internal/server/`            | AGPLv3           | Bulk of the controller logic. |
| `management/cmd/`        | REPLACE    | `apps/controller/cmd/controller/`             | AGPLv3           | We have our own cobra-based entrypoint (see issue #M). |
| `signal/`                | KEEP       | `apps/controller/internal/signal/`            | AGPLv3           | Signaling server, fold into controller binary or keep separate — decide in #6. |
| `signal/cmd/`            | REPLACE    | merged into controller                        | AGPLv3           | We don't ship a separate signal binary in Phase 1. |
| `relay/`                 | KEEP       | `apps/relay/internal/`                        | AGPLv3           | DERP-style relay implementation. |
| `relay/cmd/`             | REPLACE    | `apps/relay/cmd/relay/`                       | AGPLv3           | New cobra-based entrypoint. |
| `encryption/`            | KEEP       | `clients/core/internal/encryption/`           | AGPLv3 (interim) | Curve25519 helpers. Used by both client and controller. |
| `route/`                 | KEEP       | `clients/core/internal/route/`                | AGPLv3 (interim) | Route table management. |
| `dns/`                   | KEEP       | `clients/core/internal/dns/`                  | AGPLv3 (interim) | MagicDNS-equivalent. |
| `iface/`                 | KEEP       | `clients/core/internal/iface/`                | AGPLv3 (interim) | WireGuard interface mgmt. Critical, hard to rewrite. |
| `formatter/`             | KEEP       | `pkg/log/formatter/`                          | AGPLv3 (interim) | Log formatter. Tiny. |
| `version/`               | REPLACE    | `pkg/version/`                                | Apache 2.0       | Trivial. Clean-room rewrite. |
| `monotime/`              | KEEP       | `pkg/monotime/`                               | AGPLv3 (interim) | Monotonic time helpers. |
| `shared/`                | KEEP       | `pkg/shared/`                                 | AGPLv3 (interim) | Shared types. |
| `util/`                  | KEEP (selective) | `pkg/util/`                              | AGPLv3 (interim) | Audit each subpackage; many are general utilities. |
| `release_files/`         | STRIP      | —                                             | —                | NetBird-branded packaging assets. |
| `infrastructure_files/`  | STRIP      | —                                             | —                | Replaced by our `infra/terraform/` and `infra/helm/`. |
| `.github/`               | STRIP      | —                                             | —                | We use our own workflows. |
| `.golangci.yml`          | REFERENCE  | use as starting point                         | —                | We will adapt to our own standards. |
| `.goreleaser.yaml`       | REFERENCE  | `clients/linux/.goreleaser.yaml`              | —                | Adapt for our own client packaging. |
| `Dockerfile*`            | STRIP      | —                                             | —                | We write our own Dockerfiles per app. |
| `Makefile`               | REFERENCE  | use as inspiration                            | —                | Our root Makefile is already different (monorepo orchestrator). |
| `docs/`                  | STRIP      | —                                             | —                | We write our own. |
| `version.go`             | REPLACE    | `pkg/version/version.go`                      | Apache 2.0       | One-liner; trivial rewrite. |
| `LICENSE`                | REFERENCE  | preserved at root and per-AGPL directory      | —                | Already in place at our root. |
| `README.md`              | STRIP      | —                                             | —                | We have our own. |
| `CONTRIBUTING.md`        | STRIP      | —                                             | —                | We have our own. |
| `SECURITY.md`            | STRIP      | —                                             | —                | We have our own. |
| `CODE_OF_CONDUCT.md`     | STRIP      | —                                             | —                | We have our own. |
| `LICENSES/`              | STRIP      | —                                             | —                | We have our own LICENSE-* and LICENSING.md. |
| `idp/`                   | KEEP       | `apps/controller/internal/idp/`               | AGPLv3           | OIDC provider integrations (dex etc.). Useful for Sprint 2 #11. |
| `tools/`                 | REFERENCE  | adapt selectively into `tools/`               | Apache 2.0 (clean) | Dev tools; rewrite the bits we use. |
| `stun/`                  | KEEP       | `apps/relay/internal/stun/`                   | AGPLv3           | STUN server, paired with relay. |
| `proxy/`                 | STRIP      | —                                             | —                | TCP/HTTP proxy feature; not in Phase 1 scope. |
| `base62/`                | KEEP       | `pkg/base62/`                                 | AGPLv3 (interim) | Tiny encoding helper. |
| `sharedsock/`            | KEEP       | `clients/core/internal/sharedsock/`           | AGPLv3 (interim) | Shared raw-socket abstraction (NAT traversal helper). |
| `upload-server/`         | STRIP      | —                                             | —                | Log / file upload server; not in Phase 1 scope. |
| `combined/`              | STRIP      | —                                             | —                | Combined-binary build target. We package per-app. |
| `flow/`                  | DEFER      | `apps/ai/flow/` (Phase 2)                     | AGPLv3 (when adopted) | Flow-log collection; lines up with our AI/anomaly story but Phase 2. |
| `.githooks/`             | STRIP      | —                                             | —                | We use our own pre-commit / pre-push hooks. |
| `.devcontainer/`         | STRIP      | —                                             | —                | VS Code devcontainer; we ship our own dev story via `make dev`. |

---

## Brand and identifier hygiene

Before any import is committed to `main`, run a grep audit for these strings
and replace or remove every occurrence:

- `netbird` (case-insensitive)
- `NetBird`
- `wiretrustee` (NetBird's former name)
- `wt-` (legacy prefix)
- NetBird-specific URLs: `netbird.io`, `app.netbird.io`, GitHub orgs
- NetBird logos / image assets

Suggested check (run before each PR):

```bash
git diff --name-only main..HEAD | xargs grep -lEi 'netbird|wiretrustee|wt-' | grep -v '^docs/' || echo "clean"
```

Allowed exception: `docs/` may reference NetBird for attribution, ADR
context, and SOP citations.

---

## Import order

Follow this order so dependencies resolve cleanly:

1. `pkg/shared/`, `pkg/util/`, `pkg/monotime/`, `pkg/log/formatter/` (no deps)
2. `pkg/version/` (REPLACE — clean-room)
3. `clients/core/internal/encryption/` (used by everyone)
4. `clients/core/internal/iface/`, `clients/core/internal/dns/`, `clients/core/internal/route/`
5. `clients/core/internal/...` (the rest of the agent)
6. `apps/controller/internal/server/` (depends on shared types)
7. `apps/controller/internal/signal/`
8. `apps/relay/internal/`

Each step is its own PR. Do not bundle.

---

## Per-import PR checklist

For every import PR, the description must include:

- [ ] Upstream commit SHA the import is taken from
- [ ] List of files added (`git diff --stat`)
- [ ] License header verified (SPDX identifier in each file)
- [ ] Brand-string grep clean (per audit script above)
- [ ] Import-path rewrite applied (`github.com/netbirdio/netbird` →
      `github.com/hanfour/bamboo/...`)
- [ ] `go build` passes for affected modules
- [ ] `ORIGIN` file updated in target directory
- [ ] `docs/development/upstream-deviations.md` updated if any change vs upstream

---

## Risks specific to import

| Risk                                                  | Mitigation                                       |
| ----------------------------------------------------- | ------------------------------------------------ |
| Hidden dependencies between "stripped" and "kept"     | Build after each import; fix or restore as needed |
| AGPL leak into Apache packages                        | CI lint: ban AGPL imports from Apache-tagged dirs |
| Upstream relicenses to a non-AGPL OSI license         | Track upstream announcements; revisit ADR 0001   |
| Subtle behavior change during import-path rewrite     | Run `go test ./...` after each import; record failures |
| Trademark string missed in audit                      | Pre-merge CI check + manual review for first 3 imports |

---

## Open questions to resolve before Sprint 1 starts

- [ ] Whether to merge `signal/` into the controller binary or keep separate
- [ ] Which subpackages of `util/` we adopt vs reimplement
- [ ] Whether to commit the AGPL relay code or rewrite (relay is small enough
      to clean-room — discuss in #4)
- [ ] Whether `idp/` is worth adopting given we're already building our own
      auth flow in `apps/controller/internal/auth/` (decide before Sprint 2 #11)
- [ ] Whether `flow/` becomes the basis of our AI telemetry pipeline
      ([ADR 0010](../adr/0010-llm-multi-provider-strategy.md)) or is replaced

## References

- [NetBird Fork SOP](./netbird-fork-sop.md)
- [ADR 0001 — License Strategy](../adr/0001-license-strategy.md)
- [ADR 0011 — Client Core Re-licensing Path](../adr/0011-client-core-relicensing-path.md)
- [GitHub Issue #2](https://github.com/hanfour/bamboo/issues/2)
