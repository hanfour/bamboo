# Architecture Decision Records

We record significant architecture decisions here using the format from
[Michael Nygard's article](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).

## Index

- [0001 — License Strategy](./0001-license-strategy.md)
- [0009 — Cloud Provider Strategy](./0009-cloud-provider-strategy.md)
- [0010 — LLM Multi-Provider Strategy](./0010-llm-multi-provider-strategy.md)
- [0011 — Client Core Re-licensing Path](./0011-client-core-relicensing-path.md)
- [0012 — Phase 1 → Phase 2 Transition](./0012-phase-2-transition.md)

> ADR numbers 0002–0008 are reserved for future decisions (monorepo tooling,
> Go version, database, gRPC, multi-tenancy, region strategy, CI platform).

## Process

1. Copy [`template.md`](./template.md) to `NNNN-short-title.md`.
2. Set status to `Proposed`.
3. Open a PR for discussion.
4. On merge, status moves to `Accepted`.
5. ADRs are immutable. To change a decision, write a new ADR that supersedes
   the old one.

## When to write an ADR

Write an ADR when the decision affects:

- Public APIs or wire protocols
- Database schema or storage layout
- Security model
- Cross-module contracts
- License or governance
- Choice of major dependency or framework
