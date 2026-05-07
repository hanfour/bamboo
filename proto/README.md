# proto

gRPC protobuf definitions, shared between clients and the controller.

**License:** Apache 2.0 — see [LICENSE-APACHE](../LICENSE-APACHE).

## Layout

```
proto/
  buf.yaml              # buf configuration (lint + breaking)
  buf.gen.yaml          # buf code-generation config
  bamboo/v1/
    auth.proto          # OIDC login, sessions, pre-auth keys
    coordinator.proto   # peer registration, network topology, watch stream
    policy.proto        # ACL, evaluation, AI recommendations
    telemetry.proto     # connection events, peer metrics
  gen/go/               # generated Go code (output of `make proto`)
```

## Tooling

We use [buf](https://buf.build/) for linting, breaking-change detection,
and code generation.

```bash
# from repo root
make proto

# or directly from this directory
cd proto
buf lint
buf generate
buf breaking --against '.git#branch=main'
```

## Versioning

Package: `bamboo.v1`. All breaking changes ship as `bamboo.v2`, never as
modifications to v1. Field additions in v1 are allowed (backward-compatible);
removals and renames are not.

## Code generation outputs

- Go: `proto/gen/go/bamboo/v1/*.pb.go` — imported by both
  `apps/controller` and `clients/core`.
- TypeScript and Python SDKs are generated separately by their own
  `buf.gen.yaml` (added under `sdks/typescript/` and `sdks/python/` when
  those modules come online).

## Style

- `snake_case` for fields, `PascalCase` for messages and services.
- Use `google.protobuf.Timestamp` for absolute time.
- Pagination follows [AIP-158](https://google.aip.dev/158)
  (`page_size` / `page_token` / `next_page_token`).
- Filter expressions follow [AIP-160](https://google.aip.dev/160).
