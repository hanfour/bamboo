# Architecture Decision Records

We record significant architecture decisions here using the format from
[Michael Nygard's article](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).

## Index

- [0001 — License Strategy](./0001-license-strategy.md)

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
