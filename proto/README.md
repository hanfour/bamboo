# proto

gRPC protobuf definitions, shared between clients and the controller.

**License:** Apache 2.0 — see [LICENSE-APACHE](../LICENSE-APACHE).

## Status

Pre-alpha scaffolding. First definitions ship in Sprint 2.

## Layout (planned)

```
proto/
  bamboo/v1/
    auth.proto         # login, token exchange
    coordinator.proto  # peer registration, key rotation
    policy.proto       # ACL push, evaluation traces
    telemetry.proto    # connection events
```

## Generation

```bash
make proto
```
