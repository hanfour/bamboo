# 10-minute demo: ACL enforcement on the wire

This walkthrough proves the zero-trust promise is real — `bamboo` does not just *display* ACL rules in a Web UI, the controller actually computes per-peer `AllowedIps` from the rule set and the WireGuard config a peer receives reflects what it is permitted to reach.

Read the [Phase-1 walkthrough](development/phase-1-demo-walkthrough.md) for a wider tour that also covers OIDC sign-in, the AI recommendations cards, and the Web UI rendering. This demo is the **headline** assertion — packets are gated by policy.

## What you need

- `make local-up` running locally (Postgres + ClickHouse + controller + Web + relay on Docker).
- `make local-bootstrap` completed (migrations applied, default tenant exists).
- `grpcurl` ([install](https://github.com/fullstorydev/grpcurl)) — the policy mutation endpoint is gRPC-only by design.
- `jq` and `openssl` — the script uses them to manipulate JSON and generate WireGuard pubkeys.

## Run the script

```bash
./scripts/demo.sh
```

Annotated walk through what it does and what each section proves:

### 1. Register two peers

The script `POST`s to `/api/v1/peers/register` twice with distinct WireGuard pubkeys. The controller assigns each a `100.64.0.x` IP from the tenant's pool and returns the full peer set the caller should configure for WireGuard.

```
==> 1. register two peers (REST /api/v1/peers/register)
  ✓ dev-laptop  id=…  ip=100.64.0.1
  ✓ db-server   id=…  ip=100.64.0.2
```

No actual WireGuard interfaces are brought up — the demo asserts the controller's behavior. Real clients on Linux (`bamboo up`) or macOS/iOS (the Apple app) drive the same REST endpoint.

### 2. Tag peers

```
==> 2. tag peers (PATCH /api/v1/peers/{id})
  ✓ dev-laptop  tags=[dev]
  ✓ db-server   tags=[db]
```

Tags are tenant-scoped labels stored in `peer_tags` and surfaced on every peer read. Future ACL rules will reference them as `tag:dev` and `tag:db`.

### 3. Baseline — no policy yet

The script re-registers `dev-laptop` (idempotent on the same pubkey) and inspects the response. With no policy authored:

```
==> 3. baseline (no policy authored)
  PolicyRevision: 0    (0 = no policy authored → full mesh)
  dev-laptop's view of db-server.allowedIps: []
  ✓ revision is 0 (DB-backed lookup, was hardcoded 1 pre-v0.1.4)
  ✓ AllowedIps empty in fallback mode (client uses peer.ip/32)
```

Two things to notice:

- `PolicyRevision == 0` is the new "no policy authored" sentinel. Pre-v0.1.4 the controller hardcoded `1`; v0.1.4 reads from `acl_policies.revision`.
- `allowedIps` is empty. When `PolicyRevision == 0` the client falls back to a single `/32` per peer (full mesh). This is the pre-enforcement default and preserves backwards compatibility.

### 4. Author the policy

```
==> 4. author policy via gRPC PutPolicy
  ✓ policy revision 1 (1 rule)
```

The script `grpcurl`s `bamboo.v1.PolicyService.PutPolicy` with one HCL rule:

```hcl
rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:*"]
}
```

`bamboo`'s ACL evaluator is **first-match-wins with implicit default-deny**: once any rule exists, every (src, dst) pair the rules do not cover is denied. There is no reverse rule for `db → dev`.

The handler:

1. Validates the HCL through `policy.Parse`.
2. Commits to `acl_policies` with `revision = max(prev, 0) + 1`.
3. Writes an `audit_log` row.
4. Publishes a `PolicyChanged` event on the internal events bus — every open `WatchPeers` stream receives it immediately and reconciles by re-Registering.

### 5. Enforcement — re-register and read AllowedIps

```
==> 5. enforcement — re-register both peers, observe AllowedIps
  dev-laptop PolicyRevision after policy: 1
  dev-laptop's view of db-server.allowedIps: ["100.64.0.2/32"]
  ✓ dev → db ALLOWED (AllowedIps = [100.64.0.2/32])
  db-server's view of dev-laptop.allowedIps: null
  ✓ db → dev DENIED (AllowedIps empty; no reverse rule)
```

This is the demo's payoff:

- `dev-laptop` re-Registers. The controller looks up the current policy (revision 1), iterates over every other peer in the tenant, runs `policy.Allow(p, devView, dbView)` for each, and returns `db-server.AllowedIps = ["100.64.0.2/32"]` because the rule permits `dev → db`.
- `db-server` re-Registers. The controller runs `policy.Allow(p, dbView, devView)` and there is no rule whose source is `tag:db`. Default-deny fires. `dev-laptop.AllowedIps` is empty.

A compliant WireGuard client (the Go CLI in `clients/cli` and the upcoming Apple-side change) interprets empty AllowedIps as "drop this peer from my WireGuard config entirely". The peer's public key is not added, so packets to its tunnel IP cannot leave the local interface.

### 6. Audit trail

```
==> 6. audit trail (POST /api/v1/activity)
  2026-05-12 14:40:40 policy.update    policy
  2026-05-12 14:40:40 peer.update      peer    <db_id>
  2026-05-12 14:40:40 peer.update      peer    <dev_id>
  2026-05-12 14:40:40 peer.register    peer    <db_id>
  2026-05-12 14:40:40 peer.register    peer    <dev_id>
```

Every mutating call left an `audit_log` row: registrations, tag updates, policy update. The Web UI's dashboard (`/`) consumes the same feed via `GET /api/v1/activity`.

## What this does NOT yet prove

- **Port-level enforcement.** The rule `allow tag:dev → tag:db:*` is intentionally wildcard-port. WireGuard's `AllowedIPs` is L3-only, so the wire layer cannot filter port 5432 from port 80 once a peer is in the allowed set. Port-level enforcement is P1 follow-up work and will need a host-level firewall above WireGuard.
- **`user:` / `group:` matchers.** OIDC identity is not yet propagated to the coordinator, so rules using `user:alice@example.com` or `group:engineering` will not contribute at the wire layer in v0.1.4. They evaluate fine in the policy preview and in the EvaluateAccess RPC.
- **OIDC sign-in.** The script runs against `X-Tenant-Slug: default` (the dev-fallback path). In production with `BAMBOO_REQUIRE_AUTH=true` the same REST calls return 401 unless they carry a session JWT.
- **Live re-application without re-Register.** The script demonstrates the new state by re-Registering. Real clients receive the new state automatically via the `PolicyChanged` watch event, but verifying that requires a running WireGuard interface — out of scope for a 30-second demo.

## Teardown

```bash
./scripts/demo.sh --teardown
```

Deletes the two demo peers and clears the policy back to revision 0.

## Quick reference: what changed in v0.1.4

| Layer | Pre-v0.1.4 | v0.1.4 |
| --- | --- | --- |
| `Coordinator.Register` | returns `PolicyRevision: 1` hardcoded; never sets `Peer.AllowedIps` | reads revision from `acl_policies`; populates `AllowedIps` per recipient from `policy.Allow` |
| Heartbeat | reports `currentPolicyRevision: 1` always | reports the DB revision; tells the client to refetch when stale |
| `PolicyService.PutPolicy` | DB write only | DB write + `PolicyChanged` event publish on the bus |
| `Client.RunWatchPeers` | ignored `PolicyChanged` | re-Registers via the supplied Refresher callback |
| `wg.BuildDeviceConfig` | always `/32` per peer | uses `peer.AllowedIps` when `PolicyRevision > 0`; falls back to `/32` otherwise |
| REST `/api/v1/peers/register` JSON | (same handler, no AllowedIps in proto) | propagates `allowedIps` field to the JSON response |
