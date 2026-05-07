# Licensing

This repository is a monorepo with **dual-license** components.

## Apache License 2.0

Used for components designed for **broad embedding and integration**:

- `clients/**` — agent, CLI, mobile, native apps
- `sdks/**` — language SDKs (Go, TypeScript, Python)
- `proto/**` — gRPC definitions
- `infra/terraform/**` — Terraform examples
- `infra/helm/**` — Helm chart

Full text: [LICENSE-APACHE](./LICENSE-APACHE)

## GNU Affero General Public License v3.0 (AGPLv3)

Used for **server-side components** to prevent unauthorized commercial SaaS forks:

- `apps/controller/**` — control plane
- `apps/relay/**` — relay server
- `apps/web/**` — admin web UI
- `apps/ai/**` — AI / ML layer

Full text: [LICENSE-AGPL](./LICENSE-AGPL)

## Documentation

- `docs/**` — Creative Commons Attribution 4.0 International (CC-BY-4.0)

## Commercial Licensing

If AGPLv3 obligations conflict with your use case (e.g. embedding the control
plane in a closed-source product, or running a managed service that cannot
disclose modifications), commercial licenses are available. Contact the
maintainers.

## Contributions

By contributing, you agree to the terms of the [Contributor License
Agreement](./CONTRIBUTING.md#contributor-license-agreement). This allows the
project to offer commercial licenses while keeping the source open.
