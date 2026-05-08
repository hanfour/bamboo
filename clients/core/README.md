# clients/core

Shared client logic: WireGuard interface management, ICE-style NAT traversal,
controller communication.

**License:** AGPLv3 (interim — see [ADR 0011](../../docs/adr/0011-client-core-relicensing-path.md)).
The target license is Apache 2.0; module-local notice in [LICENSE](./LICENSE).

## Status

Pre-alpha skeleton. Currently provides:

- gRPC client wrapper around all four controller services
- `dev-agent` development binary that connects, calls `Register`, and reports
  the controller's response (Unimplemented while handlers are stubs)

## Build and run

```bash
# from repo root
make build

# in one terminal: bring up Postgres + Redis, run migrations, start controller
make dev
./bin/controller migrate up --config=apps/controller/config/example.yaml
./bin/controller serve --config=apps/controller/config/example.yaml
```

### Path A — tenant-slug fallback (zero-config)

Quickest path for local smoke tests. The default `default` tenant is
auto-created on first registration.

```bash
./bin/dev-agent --hostname=alpha
./bin/dev-agent --hostname=beta
# expected output:
#   level=INFO msg="connected to controller" addr=localhost:8080
#   level=INFO msg="authenticating via tenant-slug fallback" tenant=default
#   registered: peer_id=... ip=100.64.0.1 tenant=... peers_in_set=0
```

### Path B — pre-auth key (matches production behaviour)

Mint a key via the Auth gRPC service, then present it on register.

```bash
# 1. issue a key via grpcurl (a friendly CLI helper is on the roadmap)
SECRET=$(grpcurl -plaintext \
    -H "x-tenant-slug: my-team" \
    -d '{"description":"dev","reusable":true}' \
    localhost:8080 bamboo.v1.AuthService/CreatePreAuthKey \
  | jq -r '.secret')

# 2. register using the key — note no --tenant flag needed
./bin/dev-agent --auth-key="$SECRET" --hostname=alpha
# expected output:
#   level=INFO msg="authenticating with pre-auth key"
#   registered: peer_id=... ip=100.64.0.1 tenant=...
```

When `--auth-key` is set, `--tenant` is ignored; the tenant comes from
the redeemed key.

## Stack

- Go 1.23+
- gRPC client (`google.golang.org/grpc`)
- `wgctrl` for WireGuard kernel/userspace (Sprint 2 — not yet wired)
- `pion/ice` for NAT traversal candidates (Sprint 2)

## Layout

```
clients/core/
  cmd/dev-agent/       development-only smoke binary
  internal/client/     gRPC connection + service stubs
  internal/iface/      WireGuard interface management (Sprint 2)
  internal/route/      route table sync (Sprint 2)
  internal/dns/        DNS handling (Sprint 2)
  internal/encryption/ Curve25519 helpers (Sprint 2)
  LICENSE              interim AGPL notice
```

## Tracking

- [Sprint 1 — Issue #6](https://github.com/hanfour/bamboo/issues/6) gRPC proto
- [Sprint 2 — Issue #13](https://github.com/hanfour/bamboo/issues/13) Peer registration handshake
- [Sprint 2 — Issue #14](https://github.com/hanfour/bamboo/issues/14) Long-poll peer set updates
- [Sprint 2 — Issue #15](https://github.com/hanfour/bamboo/issues/15) First end-to-end tunnel
