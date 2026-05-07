# 0001. License Strategy

- **Status**: Accepted
- **Date**: 2026-05-07
- **Deciders**: founders

## Context

We are building an open-source mesh VPN with a planned commercial SaaS offering.
Our license choices must:

1. Allow broad adoption of client and SDK code (developers must trust the source).
2. Prevent third parties from launching competing managed services without
   contributing back (protect the SaaS business).
3. Allow us to offer commercial licenses to enterprise customers who cannot
   accept copyleft.
4. Be recognized as Open Source by the OSI (avoid licenses like SSPL or BSL
   that damage developer trust).

## Decision

Adopt a **dual-license** model:

| Component                                  | License (target) | License (interim, see note) |
| ------------------------------------------ | ---------------- | --------------------------- |
| `clients/core/**`                          | Apache 2.0       | **AGPLv3** (NetBird-derived) |
| `clients/cli/**`, `clients/macos/**`, `clients/windows/**`, `clients/linux/**` | Apache 2.0 | Apache 2.0 (clean room) |
| `sdks/**`, `proto/**`                      | Apache 2.0       | Apache 2.0                  |
| `apps/**` (controller, relay, web, ai)     | AGPLv3           | AGPLv3                      |
| `infra/terraform/**`, `infra/helm/**`      | Apache 2.0       | Apache 2.0                  |
| `docs/**`                                  | CC-BY-4.0        | CC-BY-4.0                   |

> **Interim constraint**: `clients/core` will inherit AGPLv3 from NetBird
> during the bootstrap phase (see [NetBird Fork SOP](../development/netbird-fork-sop.md)).
> The transition path to Apache 2.0 is tracked in
> [ADR 0011 — Client Core Re-licensing Path](./0011-client-core-relicensing-path.md).
> Until that ADR's exit criteria are met, anything embedding `clients/core`
> must comply with AGPLv3.

All contributors sign a CLA based on Harmony 1.0 with **Outbound Option 5**,
allowing the project to relicense contributions under any terms.

## Consequences

### Positive

- AGPLv3 prevents unauthorized SaaS forks (cloud providers must contribute back
  or buy a commercial license).
- Apache 2.0 on clients includes a patent grant, encourages embedding.
- CLA enables commercial licensing without contributor friction later.
- Both licenses are OSI-approved.

### Negative / Trade-offs

- Some enterprises reject AGPLv3 outright; we must offer commercial licenses.
- CLA adds friction for first-time contributors.
- AGPLv3 obligations apply to operators of self-hosted instances; documentation
  must clarify.

### Neutral

- Aligns with the NetBird precedent (which also moved to AGPLv3 in 2024).

## Alternatives Considered

- **MIT / BSD-3 only**: Simpler, but allows hyperscalers to launch competing
  managed services with zero obligation.
- **SSPL (MongoDB)**: Stronger SaaS protection, but not OSI-recognized;
  damages developer trust and excludes us from many corporate procurement lists.
- **BSL (HashiCorp)**: Time-delayed open source; widely perceived as
  "fauxpen source".
- **AGPLv3 everywhere**: Discourages embedding clients in third-party products.

## References

- [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0)
- [GNU AGPLv3](https://www.gnu.org/licenses/agpl-3.0.html)
- [Harmony Agreements](http://harmonyagreements.org/)
- [NetBird's AGPLv3 announcement](https://netbird.io/knowledge-hub/netbird-agpl-announcement)
