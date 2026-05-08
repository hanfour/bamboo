# NetBird Fork SOP

This document describes the standard procedure for bootstrapping bamboo's
codebase from the [NetBird](https://github.com/netbirdio/netbird) project.

> **Status:** internal SOP, pre-alpha. Adjust as we learn.

## Why fork NetBird

- AGPLv3-licensed, compatible with our control-plane license choice (see
  [ADR 0001](../adr/0001-license-strategy.md)).
- Production-ready WireGuard mesh, OIDC, signal/management split, ICE-based
  NAT traversal — saves an estimated 4–6 months of foundation work.
- Active maintenance and clear architecture (see
  [DeepWiki overview](https://deepwiki.com/netbirdio/netbird)).

## Legal preconditions (must do before first commit)

- [ ] License compliance: every directory we adopt from NetBird must retain
      its original LICENSE notice. Add an `ORIGIN` file recording the upstream
      commit SHA.
- [ ] CLA: contributors signing our CLA does not retroactively cover NetBird
      authors. We treat NetBird code as upstream and contribute fixes back.
- [ ] AGPLv3 propagation: any AGPL-derived module must remain AGPL in our tree;
      we cannot relicense those files. New files we author go in our own
      directories under our chosen license.

## High-level mapping

```
netbird/                           bamboo/
├── client/             →          clients/core/         (Apache 2.0 — see note)
├── management/         →          apps/controller/      (AGPLv3)
├── signal/             →          apps/controller/signal/ (AGPLv3)
├── relay/              →          apps/relay/            (AGPLv3)
├── encryption/         →          clients/core/internal/encryption/
├── route/              →          clients/core/internal/route/
├── dns/                →          clients/core/internal/dns/
└── version/            →          clients/core/internal/version/
```

> **Important note on `client/`**: NetBird's client is AGPLv3. We cannot
> simply re-license it as Apache 2.0. Two options:
>
> 1. **Keep client AGPLv3 short-term**, document the constraint, and replace
>    or rewrite over Phase 1–2 with our own client (eventually Apache 2.0).
> 2. **Write our own client from scratch**, using NetBird only as reference,
>    starting with our own scaffolding under Apache 2.0.
>
> Recommended: **option 1** for speed, with a tracked roadmap to option 2.
> Update [ADR 0001](../adr/0001-license-strategy.md) to reflect that
> `clients/core` is **transitively AGPLv3** until the rewrite is complete.

## Step-by-step

### 1–2. Sandbox clones (automated)

Run the helper:

```bash
./scripts/netbird-prep.sh
```

It creates `~/scratch/bamboo-fork/{netbird-upstream,netbird-working}` and
records the upstream SHA in `~/scratch/bamboo-fork/UPSTREAM_SHA`. Re-runs
fetch the latest upstream without destroying state.

Override the location with `SANDBOX_ROOT=/some/path ./scripts/netbird-prep.sh`.

### 3. Inventory what to keep / strip

Run the audit script:

```bash
./scripts/netbird-audit.sh
```

It reports:

1. The total count of files containing NetBird brand identifiers — useful
   as a baseline before scrubbing.
2. Top-level directories present upstream but missing from
   [the inventory](./netbird-import-inventory.md). Any drift must be
   resolved (add a row, then commit) before any import lands on `main`.

The inventory document is the source of truth for what we KEEP, STRIP,
REPLACE, REFERENCE, or DEFER. Update it whenever upstream adds a new
top-level directory.

### 4. Bring code into the bamboo monorepo

For each module we keep, create a feat/import branch in bamboo and copy
the files from the NetBird working clone:

```bash
cd ~/Projects/bamboo
git checkout -b feat/import-netbird-management

# Copy the management server into apps/controller/, preserving git history
# is not feasible across repos without git-filter-repo; use a clean copy
# and record the upstream SHA in apps/controller/ORIGIN.

mkdir -p apps/controller/internal
cp -r ~/scratch/bamboo-fork/netbird-working/management/* apps/controller/internal/
cd apps/controller
echo "Imported from netbirdio/netbird@$(cat ~/scratch/bamboo-fork/UPSTREAM_SHA)" > ORIGIN
echo "Date: $(date -u +%Y-%m-%d)" >> ORIGIN

# Update the package paths
find . -name "*.go" -exec sed -i.bak 's|github.com/netbirdio/netbird|github.com/<your-org>/bamboo/apps/controller/internal|g' {} +
find . -name "*.bak" -delete
```

### 5. Adapt module path and Go workspaces

```bash
cd ~/Projects/bamboo
go work init
go work use ./apps/controller
go work use ./apps/relay
go work use ./clients/core
# repeat for each Go module
```

Each Go module gets its own `go.mod`:

```bash
cd apps/controller
go mod init github.com/<your-org>/bamboo/apps/controller
go mod tidy
```

### 6. Verify it builds

```bash
cd ~/Projects/bamboo
make bootstrap   # placeholder — wire this up to install Go, protoc, etc.
go build ./apps/controller/...
go build ./apps/relay/...
go build ./clients/core/...
```

Expect build errors from import path rewrites and from removed files. Fix
incrementally — do not commit broken builds to `main`.

### 7. Run NetBird's test suite to validate the import

```bash
go test ./apps/controller/...
```

We expect some tests to fail — particularly anything depending on stripped
directories. Mark known-broken tests with a TODO and a tracking issue.

### 8. First end-to-end smoke test

The minimum viable smoke test:

1. Start `apps/controller` with a Postgres backend (use
   `infra/docker-compose.yml`).
2. Start two `clients/core` agents on the same machine, registering with
   the controller.
3. Verify they exchange WireGuard keys and establish a tunnel.
4. Ping across the tunnel.

Capture this as `apps/controller/test/e2e/smoke_test.go`.

### 9. Document deviations

Every place we deviate from upstream NetBird gets recorded in
`docs/development/upstream-deviations.md`:

- Why we changed it
- Whether we plan to upstream it (and the issue number)
- Whether we will resync from upstream (and how)

### 10. Plan the upstream sync cadence

- Weekly: `git fetch upstream main` in the read-only sandbox.
- Monthly: triage NetBird security and bug-fix commits, cherry-pick what
  applies.
- Quarterly: review whether to take a larger upstream merge or whether we
  have diverged enough to stop tracking.

## What to strip from NetBird (explicit list)

These directories or files are not adopted into bamboo:

- `client/ui/` — replaced by our own native clients
- `release_files/` — we have our own packaging pipeline
- `infrastructure_files/` — replaced by `infra/terraform/` and `infra/helm/`
- NetBird-branded README, LOGO, and marketing assets
- NetBird's `.github/workflows/` — replaced by ours
- `docs/` — we write our own

## Risks and mitigations

| Risk                                          | Mitigation                                          |
| --------------------------------------------- | --------------------------------------------------- |
| AGPL viral propagation broader than expected  | Legal review before first commit; ADR update        |
| Upstream NetBird relicenses or pivots         | We are AGPL-fork-tolerant; can continue independently |
| Hidden trademark / branding strings remain    | Grep audit before each release: `netbird`, `NetBird`, `wiretrustee` |
| Security advisory in upstream we miss         | Subscribe to NetBird's security advisory feed       |
| Diverged code becomes hard to merge           | Quarterly upstream sync review; ADR on major divergence |

## References

- [NetBird upstream](https://github.com/netbirdio/netbird)
- [NetBird architecture overview](https://docs.netbird.io/about-netbird/how-netbird-works)
- [NetBird DeepWiki](https://deepwiki.com/netbirdio/netbird)
- [ADR 0001 — License Strategy](../adr/0001-license-strategy.md)
- [git-filter-repo (for if we ever need history preservation)](https://github.com/newren/git-filter-repo)
