# NAT64 Phase C1 — egress approval + ACL route synthesis (2026-05-30)

First sub-project of NAT64 Phase C (the translator data plane) from the
umbrella (`docs/design/2026-05-27-nat64-architecture.md` §3/§5). Phase C
is decomposed into four sub-projects:

- **C1 (this doc):** egress capability reporting + admin approval + ACL
  route synthesis. Pure controller / CLI / Web — unit-testable. The
  routing scaffolding that makes an approved egress reachable.
- **C2:** the actual Jool/Tayga translator on the Linux egress peer.
- **C3:** health probing + multi-egress re-routing.
- **C4:** the DNS64 client (the deferred Phase B PR 2 — Apple
  `NEDNSProxyProvider` external DNS64).

C1 ships the admin/routing half: an admin can designate a peer as the
tenant's NAT64 egress, and every other peer that the ACL lets reach it
automatically routes `<nat64_prefix>::/96` translation traffic to it.
The egress doesn't actually translate yet (C2), so this is scaffolding
— but it is fully unit-testable and mirrors the shipped exit-node /
subnet-router admin model (#149).

## 1. Scope

- **Capability:** CLI `--advertise-nat64-egress` → register reports
  `peers.nat64_egress_capable`.
- **Approval:** admin `POST /api/v1/peers/{id}/nat64-egress/approve` →
  `peers.nat64_egress_approved`. Mirrors exit-node approval.
- **ACL route synthesis:** when `tenant.dns64_enabled` AND a peer is
  `nat64_egress_approved`, every other ACL-permitted peer's view of that
  egress gains `<resolved nat64_prefix>::/96` in `AllowedIPs` (enforce +
  simulate paths, kept consistent).
- **Web:** an admin "NAT64 egress" approval toggle, parallel to the
  exit-node toggle.

No schema migration — the hook columns (`peers.nat64_egress_capable`,
`peers.nat64_egress_approved`, `tenants.nat64_prefix`) already exist
from Phase A migration 00018, and `tenants.dns64_enabled` from Phase B
migration 00019.

## 2. Out of scope (C1)

- The Jool/Tayga translator data plane (C2). The synthesised route
  points at an egress that does not translate yet — C1 is routing
  scaffolding only.
- Health probing + multi-egress active selection (C3). C1 deterministically
  picks a single active egress when more than one is approved.
- The DNS64 client (C4). C1's route is exercised by manually targeting
  `<prefix>::/96` until C4/Phase-B-DNS64 generates it automatically.
- Observability labels (`nat64_translated` audit/Prometheus) — Phase D.

## 3. Cross-phase decisions inherited

- `dns64_enabled` (tenant, Phase B) is the **tenant-level NAT64 master
  switch**. The egress route is emitted only when it is true — matching
  the Phase B route-conflict exemption, which is also gated on
  `dns64_enabled`. `nat64_egress_approved` (peer) is the per-peer
  "this box is the translator" designation. Both must hold for routing.
- Route model is **automatic / tenant-wide**: an approved egress is a
  shared translation service, not a per-peer-elected default route (so,
  unlike exit-node, there is no `UsingNAT64EgressPeerID` opt-in field).
- NAT64 prefix resolution + the `/96` validation + the route-conflict
  exemption already landed in Phase B (`internal/nat64`, #236).

## 4. Capability reporting

Mirrors exit-node (`--advertise-exit-node` → `exit_node_capable`).
**No proto change** — exit-node capability/approval do not ride the gRPC
`RegisterRequest`/`Peer` proto; they flow through the REST register
side-channel and surface on the REST `apiPeerJSON`. C1 mirrors that.

- **REST register body** (`peerRegisterRequest`): add
  `AdvertiseNat64Egress bool json:"advertiseNat64Egress,omitempty"`.
- **register side-channel:** after the mesh-state register completes,
  persist `peers.nat64_egress_capable = body.AdvertiseNat64Egress` via a
  new `peers.SetNAT64EgressCapable` (alongside the existing
  `SetExitNodeCapable` call). The capability tracks the client's last
  register; the admin's `approved` sign-off survives a re-register that
  drops the flag (stored in a separate column).
- **REST peer JSON** (`apiPeerJSON`): add `nat64EgressCapable` +
  `nat64EgressApproved` so the Web/admin can show + gate on both states.

## 5. Admin approval

Mirrors `POST /api/v1/peers/{id}/exit-node/approve`.

- **Route:** `POST /api/v1/peers/{id}/nat64-egress`, body
  `{"approved": bool}` (matches the exit-node route `{id}/exit-node` —
  no `/approve` suffix). Admin-only (`requireAdmin`, permission
  `peer.nat64-egress.approve`); approving requires `nat64_egress_capable`.
  Writes an audit row (`peer.nat64-egress.approve`, same helper as
  exit-node). Add `"nat64-egress"` to the `normalizeRoute` peer-subpath
  list for the metrics label.
- **repo:** `Peers.SetNAT64EgressApproved(ctx, id, approved bool) error`
  — a one-column UPDATE, identical shape to `SetExitNodeApproved`.
- **Peer model:** add `NAT64EgressCapable bool` + `NAT64EgressApproved
  bool` to `repo.Peer`, read on every peer SELECT path (mirrors how
  `ExitNodeCapable`/`ExitNodeApproved` are threaded) and written by the
  register Insert (`nat64_egress_capable`).

## 6. ACL route synthesis (core)

The egress route is added in the L3 enforcement path, exactly where
exit-node's `0.0.0.0/0`/`::/0` is added.

`allowedIPsFor(p *policy.Policy, src, dst *repo.Peer, ...)` in
`handlers/coordinator.go` currently:
1. returns nil if `policy.Allow(src, dst)` is false,
2. emits dst's tunnel `/32` + `/128`,
3. appends `dst.ApprovedRoutes`,
4. appends `0.0.0.0/0`,`::/0` when src elected dst as its exit node.

C1 adds, after (3): **when the tenant has DNS64 on AND dst is the active
NAT64 egress, append `<resolved nat64_prefix>::/96`.** So a permitted
src routes its synthesised v6 translation traffic to the egress.

### Interface change

`allowedIPsFor` needs the tenant-level `dns64_enabled` + resolved
`nat64_prefix`, plus the identity of the active egress (for the
multiple-approved case, §6.1). The register loop has the tenant and the
peer set. Thread these via a small value struct, e.g.:

```go
type nat64RouteCtx struct {
    enabled     bool          // tenant.DNS64Enabled
    prefix      string        // nat64.ResolvePrefix(tenant.NAT64Prefix), e.g. "64:ff9b::/96"
    activeEgress uuid.UUID    // the single active egress peer id (uuid.Nil if none)
}
```

computed once per register (not per (src,dst) pair) and passed into
`allowedIPsFor`. In `allowedIPsFor`:

```go
if rc.enabled && rc.activeEgress != uuid.Nil && dst.ID == rc.activeEgress {
    out = append(out, rc.prefix) // already a /96 string from ResolvePrefix
}
```

The egress peer is never routed to itself (the register loop already
skips self / the existing same-peer handling), and a src the ACL denies
gets nil from `allowedIPsFor` before reaching this branch — so the route
respects the policy, exactly like exit-node.

### 6.1 Active-egress selection (single, deterministic)

If the admin approves **more than one** egress, emitting `<prefix>::/96`
for each would put the same `/96` in `AllowedIPs` pointing at two peers
— a WireGuard longest-prefix collision (the very case Phase B's
route-conflict exemption suppresses in the warning UI, but at the data
plane only one peer can own a CIDR). So C1 selects a **single active
egress deterministically**: the approved egress with the lowest peer ID
(big-endian UUID byte order). C3 replaces this with health-aware
selection (and, where the platform allows, ECMP across live egresses).

`activeEgress` is computed once per register from the tenant's peer set:
the min-ID peer that is `nat64_egress_approved` **AND itself live** —
`approval_status == "approved"` and `status != "disabled"`. The
liveness filter matters: the register loop already skips pending/disabled
peers, so selecting a dead egress would emit the `/96` route to nobody
and silently shadow a healthy higher-ID egress. (Health-*probing* — is
the live egress's translator actually up — is C3; this is just the
basic mesh-visibility filter the register loop itself applies.)

### 6.2 Simulate-path consistency — not applicable for C1

The admin policy simulator (`apiSimulatePolicy` → `dstTunnelIPs`)
currently surfaces ONLY each peer's tunnel IPs — it does not show
`ApprovedRoutes` or the exit-node `0.0.0.0/0`/`::/0` either. So, to stay
consistent with how the other route types are already absent from the
simulator, C1 adds the egress route ONLY to the enforce path
(`allowedIPsFor`), not the simulator. (Phase B's enforce/simulate
byte-identity applied to the tunnel IPs, which both paths emit; the
route types are an enforce-only concern.)

## 7. CLI

`clients/cli` gains `--advertise-nat64-egress` (bool), mirroring
`--advertise-exit-node`: it sets `advertise_nat64_egress` on the
RegisterRequest. The flag advertises *willingness*; whether the box can
actually translate (Jool present) is a C2 concern — C1's capability flag
is the advertise intent only, exactly like exit-node's capability flag.

## 8. Web

A "NAT64 egress" approval control in the admin peer surface (drawer /
review), shown only when `nat64EgressCapable`, calling
`POST .../nat64-egress/approve`. Parallel to the exit-node toggle. May
land as its own PR (PR 2) alongside the CLI flag.

## 9. Error / edge cases

- **No approved egress, DNS64 on:** no route emitted — synthesised v6
  (once C4 lands) black-holes, the documented pre-translator state.
- **Egress approved, DNS64 off:** no route emitted (master switch off).
  Approving an egress alone does nothing until DNS64 is enabled.
- **Multiple approved egresses:** single deterministic active egress
  (§6.1); others ignored with a log line until C3.
- **ACL denies src→egress:** src gets no route to the egress (policy
  wins), same as exit-node.
- **Egress peer's own view:** it does not route `<prefix>::/96` to
  itself; it is the translation target, not a client of itself.

## 10. Test plan

- `allowedIPsFor` unit tests: (a) DNS64 on + dst is active egress →
  AllowedIPs contains `<prefix>::/96` (after tunnel + approved routes);
  (b) DNS64 off → no egress route; (c) dst approved but not the active
  (min-ID) egress when multiple approved → no route; (d) ACL-denied src
  → nil. Mirror the existing `coordinator_enforce_test.go` style.
- `activeEgress` selection unit test: min-ID among approved; nil when
  none approved; a pending/disabled approved-egress is skipped in favour
  of a live higher-ID one.
- approval API test: `POST .../nat64-egress` flips
  `nat64_egress_approved`, admin-gated, audited.
- register capability test: `advertise_nat64_egress` → persisted
  `nat64_egress_capable`; survives a re-register dropping the flag while
  `approved` stays.
- proto freshness (`make proto-check`); CLI flag test; Web typecheck +
  lint.
- **Integration tests run against a real Postgres** (the CI "Unit +
  integration tests" job). Verify locally before pushing with a throwaway
  Postgres + `goose up` — repo/handlers/e2e tests touch the DB and are
  skipped by a bare `make test`.

## 11. PR breakdown (≈2)

1. **Controller** — REST register body `advertiseNat64Egress` +
   capability side-channel + approval API + `repo` field/SELECT/writer +
   ACL route synthesis (enforce path only) + unit tests. No proto change.
2. **CLI + Web** — `--advertise-nat64-egress` flag; admin approval toggle.

## 12. Phase boundary

C1 makes the egress reachable. C2 makes it actually translate (Jool on
the Linux egress). C3 makes egress selection health-aware + multi. C4
(deferred Phase B PR 2) generates the synthesised v6 traffic the route
carries. Until C2, the route points at a non-translating peer — verify
C1 purely by asserting `AllowedIPs` contents (unit) + that
`POST .../nat64-egress/approve` flips the flag.
