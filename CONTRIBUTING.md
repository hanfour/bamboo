# Contributing to bamboo

Thanks for considering a contribution. This is a pre-alpha project under active
development; expect breaking changes.

## Ground Rules

- Be kind. See our [Code of Conduct](./CODE_OF_CONDUCT.md).
- Discuss substantial changes in an issue first.
- One PR = one logical change.
- All PRs must pass CI (lint, test, build).

## Development Setup

```bash
git clone <repo-url>
cd bamboo
make bootstrap   # install toolchain dependencies
make test        # run all tests
```

See per-module READMEs under `apps/`, `clients/`, etc. for component-specific
setup.

## Branching

- `main` — protected, deployable.
- Feature branches: `<type>/<short-description>`
  - Types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`
  - Example: `feat/oidc-google-provider`

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(controller): add OIDC provider for Google
fix(relay): handle EOF on STUN socket
docs(adr): record license strategy
```

## Pull Requests

- Fill in the PR template.
- Link the issue you're solving.
- Keep PRs under ~500 lines where possible.
- Request review from a CODEOWNER.

## Contributor License Agreement

Before your first PR is merged, you must sign the project CLA.
*(CLA bot configuration pending — interim contributors will sign manually.)*

The CLA grants the project permission to relicense your contribution as part of
the dual-license strategy. You retain copyright to your work.

## Coding Standards

| Stack      | Tools                                    |
| ---------- | ---------------------------------------- |
| Go         | `gofmt`, `golangci-lint`, Effective Go   |
| TypeScript | `eslint`, `prettier`, `strict: true`     |
| Python     | `ruff`, `black`, type hints required     |

## Architecture Decision Records (ADRs)

Architectural decisions live in [`docs/adr/`](./docs/adr/). Open a PR using
[the template](./docs/adr/template.md) for any change to:

- Public APIs
- Database schema
- Security model
- Cross-module contracts
- License or governance

## Questions?

Open a discussion or reach out in the internal channel.
