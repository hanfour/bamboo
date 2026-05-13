# relay

DERP-style relay server. Forwards encrypted WireGuard packets between
peers that can't form a direct tunnel (symmetric NAT, egress-only
firewalls).

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

End-to-end working. The binary runs a WebSocket binary-frame relay
protocol (ADR 0013): clients open a TLS connection, send
`CLIENT_HELLO = pubkey + HMAC token`, and exchange `PACKET` frames
carrying `dst_pubkey + WireGuard packet`. The relay only forwards
encrypted WG packets — no plaintext exposure, WireGuard's E2E model
preserved.

Auth: HMAC tokens minted by the controller's `POST /api/v1/relay-
token`. Token claims bind to `(tenant_id, peer_id, wireguard_public_
key)` so cross-tenant routing is impossible and a peer can't
impersonate another peer's pubkey.

CLI (`clients/core/relay/`) and Apple (`clients/apple/Shared/
RelayClient.swift`) integrations both ship; the CLI's relay-fallback
monitor swaps a peer from direct → relay endpoint when handshake
stops succeeding.

## Build and run

```bash
go build -o ./bin/relay ./cmd/relay
./bin/relay serve --listen=:9443 --secret=$BAMBOO_SESSION_SECRET
```

The local compose stack (`make local-up`) brings up a relay alongside
controller + postgres + clickhouse + web.

## Stack

- Go 1.23+
- WebSocket over TLS (`gorilla/websocket`)
- HMAC-SHA256 token verification (shared secret with controller)
- In-memory `pubkey -> session` routing table, partitioned by
  `tenant_id` from the verified token

## Deployment regions (planned)

- `tpe` Taipei (primary)
- `nrt` Tokyo
- `sin` Singapore
- `icn` Seoul (Phase 2)

Multi-relay failover + per-relay bandwidth metering for capacity
planning are still deferred (ADR 0013-C).
