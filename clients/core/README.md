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
go build -o ./bin/dev-agent ./clients/core/cmd/dev-agent

# in one terminal: start the controller
./bin/controller serve --config=apps/controller/config/example.yaml

# in another terminal: connect from the dev agent
./bin/dev-agent --addr=localhost:8080
# expected output:
#   level=INFO msg="connected to controller" addr=localhost:8080
#   level=INFO msg="register returned error (expected while handlers are stubs)"
#                                                        err="rpc error: code = Unimplemented..."
```

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
