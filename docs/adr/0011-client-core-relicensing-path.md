# 0011. Client Core Re-licensing Path

- **Status**: Accepted
- **Date**: 2026-05-07
- **Deciders**: founders
- **Related**: [ADR 0001](./0001-license-strategy.md), [NetBird Fork SOP](../development/netbird-fork-sop.md)

## Context

Our license strategy ([ADR 0001](./0001-license-strategy.md)) targets
**Apache 2.0** for all client and SDK code, on the rationale that broad
embedding requires a permissive license.

However, to ship Phase 1 in 4 months we are bootstrapping `clients/core`
from NetBird, which is **AGPLv3**. AGPLv3 is copyleft and cannot be
unilaterally re-licensed: every line of code we adopt from NetBird remains
AGPLv3 unless every NetBird contributor agrees to relicense or we replace
that code with a clean-room rewrite.

This creates a temporary mismatch between our stated license target and
reality. Without an explicit transition plan, we risk:

- Misleading downstream embedders ("we said Apache, but it's actually AGPL").
- Permanent stuckness — copyleft inheritance does not resolve itself.
- Legal exposure if a customer or partner relies on Apache 2.0 terms that
  do not actually apply.

## Decision

**Adopt a phased re-licensing plan for `clients/core`:**

### Phase A — Inherit AGPL (Sprint 1 onward)

- `clients/core/**` is licensed AGPLv3 in this phase.
- LICENSE notice in `clients/core/LICENSE` references AGPLv3 explicitly.
- README and module docs clearly state the AGPLv3 status, and embedders
  are warned that AGPL terms apply.
- An `ORIGIN` file in `clients/core/` records the upstream NetBird commit
  SHA from which each subdirectory was imported.

### Phase B — Quarantine and shrink the AGPL surface (Sprints 3–8)

- Identify every file in `clients/core` derived from NetBird; mark with
  `// SPDX-License-Identifier: AGPL-3.0-or-later` and an `// origin: netbird@<sha>`
  comment.
- New code we write goes in **separate packages** within `clients/core`
  with `// SPDX-License-Identifier: Apache-2.0`. These packages must not
  import AGPL packages directly; they communicate via stable interfaces
  defined in our own packages.
- Shrink the AGPL surface incrementally: each rewrite replaces an AGPL
  package with an Apache 2.0 equivalent and updates the import graph.

### Phase C — Clean-room replacement (Phase 2 of the product roadmap)

- Replace remaining AGPL packages module-by-module with our own
  implementations.
- A package is considered "replaced" when:
  - All AGPL files are deleted from `clients/core`.
  - The replacement is implemented from public specifications and our own
    notes only — **no copy-paste from NetBird sources**.
  - A code reviewer who has never read NetBird's code signs off.
- After all packages are replaced, the AGPL `LICENSE` is removed and the
  package header license is updated to Apache 2.0.

### Phase D — Re-license declaration (Phase 3)

- ADR 0001 is updated to drop the "interim" column.
- A blog post or release note documents the transition.
- LICENSING.md is amended.

## Exit criteria for Phase D

- [ ] `clients/core/` contains zero files with AGPL SPDX identifier.
- [ ] `git log` for every file in `clients/core/` shows commits authored only
      by signatories of our CLA (ensures clean rights chain).
- [ ] An external license auditor (or law firm) reviews and confirms the
      transition.
- [ ] No remaining transitive dependency on AGPL packages from outside
      `apps/**`.

## Consequences

### Positive

- Honest representation of license state at every point in time.
- Avoids the "we'll figure it out later" trap that has bitten other
  open-core projects (e.g. Redis ↔ Valkey ↔ Microsoft Garnet ecosystem).
- Allows immediate Phase 1 ship velocity.
- Protects future commercial licensing options (clean rights chain).

### Negative / Trade-offs

- Embedders who want a permissive client license cannot ship until
  Phase D completes (Phase 3 of the product roadmap, ~12–15 months).
- Maintaining AGPL / Apache split within `clients/core` requires
  discipline; one slip and we taint the Apache packages with AGPL.
- Clean-room rewrites are slower than incremental refactors of NetBird's
  code, but mixing the two would invalidate the rights chain.

### Neutral

- Documentation overhead — ORIGIN files, SPDX headers, ADR amendments.
- This is a well-understood pattern in open-source business; references
  exist (Funtoo's Portage rewrite, ReactOS's clean-room procedures).

## Process discipline

- **Reviewers must explicitly check** that PRs do not move AGPL code into
  Apache 2.0 packages (CI lint to enforce).
- **Clean-room rewriters** must declare their NetBird-source isolation in
  the PR description (a one-line statement).
- **PR template** updated to include a license-impact checkbox.

## Alternatives Considered

- **Stay on AGPL forever**: simpler but breaks the embedding story for
  enterprise OEM partners and forces all SDK consumers onto AGPL terms.
- **Relicense via contributor consent**: contact every NetBird contributor
  and request relicense permission. Rejected — impractical at scale,
  several contributors are anonymous, and even one refusal blocks the path.
- **Fork from a permissively-licensed alternative** (e.g. Innernet, Nebula):
  rejected — neither offers comparable feature parity, and rewriting cost
  is similar to the clean-room path.
- **Commercial license from NetBird Inc.**: NetBird is itself dual-licensed
  AGPL + commercial. We could in principle buy a commercial license that
  permits us to re-license `clients/core`. Parked as a fallback option;
  not pursued first because it creates an unhealthy dependency.

## References

- [ADR 0001 — License Strategy](./0001-license-strategy.md)
- [NetBird Fork SOP](../development/netbird-fork-sop.md)
- [SPDX License Identifiers](https://spdx.dev/learn/handling-license-info/)
- [Clean-room design (Wikipedia)](https://en.wikipedia.org/wiki/Clean-room_design)
- [Funtoo's Portage rewrite (precedent for staged relicensing)](https://www.funtoo.org/Funtoo:Portage_Refresh)
