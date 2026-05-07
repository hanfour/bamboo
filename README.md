# bamboo

> AI-native zero-trust mesh networking for engineering teams.

**Status:** pre-alpha — internal development. APIs and architecture will change.

## What is this?

bamboo is a WireGuard-based mesh VPN with:

- **Code-first ACLs** — manage policy via Terraform, Pulumi, and GitOps.
- **AI-assisted security** — anomaly detection and least-privilege ACL recommendations.
- **APAC-first infrastructure** — relay servers in Taipei, Tokyo, Singapore.

## Repository Layout

```
apps/             server-side components (AGPLv3)
  controller/       control plane (auth, coordination, ACL)
  relay/            relay server (NAT traversal fallback)
  web/              admin web UI
  ai/               AI / ML layer

clients/          agents and native clients (Apache 2.0)
  core/             shared client logic
  cli/              command-line tool
  macos/            macOS native app
  windows/          Windows native app
  linux/            Linux daemon

sdks/             language SDKs (Apache 2.0)
  go/               Go SDK
  typescript/       TypeScript SDK
  python/           Python SDK

proto/            gRPC definitions (Apache 2.0)

infra/            deployment artifacts
  terraform/        IaC examples (Apache 2.0)
  helm/             self-hosted Helm chart (Apache 2.0)
  docker-compose.yml  local development stack

docs/             documentation
  adr/              Architecture Decision Records
  architecture/     system architecture and diagrams
```

## Getting Started

```bash
make help            # list available commands
make bootstrap       # install toolchain dependencies
make dev             # start local development stack
```

See per-module READMEs for component-specific instructions.

## License

Dual-licensed. See [LICENSING.md](./LICENSING.md).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). All contributors must sign the CLA.

## Security

Found a vulnerability? See [SECURITY.md](./SECURITY.md).
