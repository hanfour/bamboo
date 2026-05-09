# 0013. Relay Protocol — DERP-like for symmetric-NAT crossing

- **Status**: Accepted (server skeleton + controller registry land in
  this PR; client integration is JJJJ-followup)
- **Date**: 2026-05-09
- **Deciders**: founders
- **Supersedes**: nothing
- **Related**: ADR 0012 (Phase 2 transition); FFFF→IIII peer endpoint
  arc (#39)

## Context

After FFFF→IIII landed, two bamboo peers behind cone-NAT routers form
a direct WireGuard tunnel without a relay (STUN-discovered endpoints
work on most home / office networks). Two cases still fail:

1. **Symmetric NAT**: carrier-grade NAT on cellular networks, hotel
   Wi-Fi, some corporate networks. The NAT mapping observed by STUN
   is not the same one that arrives from a different destination, so
   the peer-side handshake can't match the public endpoint we
   distributed.
2. **Egress-only firewalls** that allow outbound TCP/443 but block
   arbitrary outbound UDP. Common in restricted offices.

For our first-user dogfood — the founders' Mac + iPhone — case (1)
hits whenever someone tunnels from a Taiwan Mobile / Chunghwa
cellular hotspot. We want a bamboo experience that works there too.

## Decision

Build a custom **DERP-like relay**: an external server that proxies
WireGuard packets between two peers identified by their WG public
keys. Inspired by Tailscale's DERP (Designated Encrypted Relay for
Packets); not protocol-compatible.

### Why DERP-like rather than TURN

- **Identity model fits**: bamboo peers are identified by Curve25519
  public keys; DERP routes by public key. TURN identifies allocations
  by IP:port pairs and would need a separate mapping table.
- **HTTPS-friendly**: DERP runs over WSS, which traverses every
  network bamboo cares about (including HTTP-proxy-only environments).
  TURN typically uses UDP/3478 which the same restrictive networks
  block.
- **End-to-end encryption preserved**: the relay only sees encrypted
  WireGuard payloads + a 32-byte destination key. No cleartext, no
  decrypted IP packets, no PII. A subnet-relay-peer alternative
  (forwarding via Linux IP forwarding on a VPS) would have plaintext
  on the relay — unacceptable for a security product.
- **Operationally simple**: one Go binary, one TLS port, one
  controller-managed peer-key allowlist. No allocation lifecycle, no
  username/credential rotation a la TURN.

### Wire protocol (v1)

WSS framing. Each frame:

```
+--------+-----+----------+
|  len   | typ | payload  |
| 4 BE   | 1B  | variable |
+--------+-----+----------+
```

`len` is the total frame size including itself.

| typ  | name           | direction       | payload                                  |
| ---- | -------------- | --------------- | ---------------------------------------- |
| 0x01 | SERVER_HELLO   | server -> client | version (1B); sent *after* registration |
| 0x02 | CLIENT_HELLO   | client -> server | client WG pubkey (32B) + auth token     |
| 0x03 | PACKET         | both directions | dst pubkey (32B) + WG packet (variable) |
| 0x04 | PEER_GONE      | server -> client | dst pubkey (32B) the client tried       |
| 0x05 | KEEPALIVE      | both directions | empty                                    |

Handshake order:

1. Client opens WSS, immediately sends CLIENT_HELLO.
2. Server validates auth, registers `pubkey -> session`, sends SERVER_HELLO.
3. Both sides exchange PACKET / KEEPALIVE freely.

This ordering makes SERVER_HELLO act as a synchronous "you are in the
routing map" ack — without it, two clients connecting back to back
can race each other on the first PACKET (the second client's session
may not be registered yet when the first sends to it).

Auth tokens (CLIENT_HELLO) are JWTs signed by the controller. Claims:
`{ tenant_id, peer_id, wg_pubkey, exp }`. The relay verifies signature
against the controller's public key (HMAC for v1; switch to RSA/EC
once we run multiple controllers).

### Routing

The relay maintains a per-tenant map `wg_pubkey -> Connection`. When
a PACKET frame arrives, the relay:

1. Looks up the destination key in the same tenant.
2. Forwards the frame as-is (the destination decodes it as a WG
   inbound packet).
3. If unknown, replies PEER_GONE to the sender.

Cross-tenant traffic is impossible: the connection map is partitioned
by tenant_id from the JWT.

### Client integration (JJJJ-followup)

Each client (Go CLI, Apple app) maintains a WSS connection to one
configured relay. Locally it exposes a UDP socket on loopback that
acts as a per-peer endpoint:

- WireGuard config gets `Endpoint = 127.0.0.1:<relayProxyPort>` for
  any peer marked as relay-only.
- The local proxy reads from this UDP socket, prepends the
  destination's WG pubkey, sends as a PACKET frame.
- Inbound PACKET frames are unwrapped and written back to WG's
  listening socket.

This is the "DERP magic" Tailscale uses; well-understood pattern.

### Controller integration (lands in this PR)

- New table `relay_servers (id, region, hostname, port, public_key,
  enabled, created_at)`.
- Controller's `Register` populates `RegisterResponse.relay_servers`
  with all `enabled=true` rows.
- New REST endpoint `POST /api/v1/admin/relays` to add a relay
  (admin-only; tenants don't manage their own relays in v1).

## Consequences

### Positive

- Symmetric NAT and egress-only firewalls become possible to cross.
- bamboo can plausibly replace Tailscale on every network type the
  user encounters.
- The protocol is small enough to audit (~600 lines server, ~400
  lines per client). DERP itself is open source; we can crib design
  decisions.
- End-to-end encryption preserved — the relay sees only encrypted WG
  payloads + destination key.

### Negative / Trade-offs

- One more service to operate. Relay servers need uptime (otherwise
  symmetric-NAT'd users disconnect when the relay goes down).
  Mitigation: clients can iterate a list of relays from the
  controller and pick the lowest-latency.
- Relay bandwidth costs. Tailscale runs DERP at significant scale;
  bamboo can start with one relay per region (Tokyo + Singapore for
  APAC) and scale up.
- Custom protocol means we maintain compatibility ourselves. We pin
  v1 here; v2 can deprecate-and-remove via the version byte in
  SERVER_HELLO.

### Neutral

- The relay binary is Apache-2.0 licensed (consistent with `clients/`
  and `infra/`); the controller stays AGPLv3.

## Alternatives Considered

### TURN via coturn

Rejected for the reasons in §"Why DERP-like rather than TURN".

### Subnet-relay peer (jump host with IP forwarding)

Rejected because the relay peer would see cleartext IP packets between
mesh peers. Acceptable for a hobbyist mesh; unacceptable for a product
sold as zero-trust.

### Use Tailscale DERP servers as a third party

Rejected: it would create a hard dependency on a competitor's
infrastructure. Educational reference only.

### Native-WG relay via PersistentKeepalive + AllowedIPs

This works for a single relay (configure all peers to also AllowAccess
to relay's tunnel IP, kernel forwards) but requires Linux IP forwarding
on the relay, plaintext exposure, and breaks WireGuard's E2E model.
Same objection as subnet-relay-peer.

## Implementation plan

In this PR (JJJJ-A):

- ADR (this document).
- Migration `00003_relay_servers.sql` with the schema above.
- `apps/controller/internal/db/repo/relays.go` repository.
- Controller `Register` populates `RegisterResponse.relay_servers`.
- `POST /api/v1/admin/relays` REST endpoint (admin-only via the
  authenticated user's `is_admin` flag).
- `apps/relay/cmd/relay/` Go binary skeleton: TLS listener, frame
  parser, in-memory session map, packet forwarder. **No JWT auth in
  this PR — dev-mode pass-through is acceptable for v0.** Auth lands
  in JJJJ-B.

Deferred to JJJJ-B (next PR / next session):

- JWT auth on relay CLIENT_HELLO.
- Go CLI relay client (`clients/core/relay/`).
- Apple Swift relay client (`Shared/RelayClient.swift`).
- TunnelManager integration: when a peer's only reachable endpoint
  is the relay, configure WireGuard endpoint to the local proxy port.

Deferred to JJJJ-C:

- Relay server health check + multi-relay failover.
- Per-relay bandwidth metering for capacity planning.
