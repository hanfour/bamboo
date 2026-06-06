# NAT64 Phase C3 — egress health probing + multi-egress failover (2026-06-06)

Third hardening sub-project of NAT64 Phase C. C1 (#238/#239) made an
approved egress reachable; C2 (#240/#241) made it translate; C4
(#242/#243/#244) gave the translator a real DNS64 consumer. All of that
routes the `<nat64_prefix>::/96` to **one deterministically-chosen** egress
(C1's `computeNAT64EgressRoute`: lowest-UUID among approved + mesh-live).
If that egress dies, NAT64 black-holes until an admin re-approves another.

C3 makes the selection **health-aware** with single-active failover: when
the active egress becomes unhealthy, traffic fails over to the next
eligible approved egress, and admins can see each egress's health.

This is hardening, not new data plane — the packet path (C1→C2) is
unchanged; only *which* egress the route points at, and how promptly that
changes, is new.

## 1. Locked decisions (from brainstorming + a 5-lens review panel)

- **Health signal = CLI self-report + staleness** (NOT controller active-probing the translator — the controller has no path to a peer's NAT64 TUN). The egress CLI reports its Tayga translator liveness on the heartbeat; the controller combines that with heartbeat freshness (`last_seen_at`).
- **Selection = single-active failover** (NOT ECMP). Still one active egress; selection becomes health-aware. `allowedIPsFor` continues to emit the `/96` to exactly one `dst`.
- **Self-report rides the REST heartbeat as a `*bool` side-channel** (NOT a gRPC proto field). The production egress is the Linux CLI, which heartbeats over `POST /api/v1/peers/heartbeat` (REST/JSON) — the gRPC `HeartbeatRequest` is an internal handler boundary the CLI never touches. The field follows the existing **ConnectionPath side-channel** precedent (`api_peers.go` — heartbeat-reported admin-visibility data, persisted via a dedicated repo call *outside* `coord.Heartbeat`).
- **`unknown` = eligible** (relay-health precedent): only a *confirmed* `unhealthy` egress is skipped. A fresh deploy before any health is known still routes.
- **Persistence + API:** two new peer columns (`nat64_egress_health_status`, `nat64_egress_health_reason`) exposed on `/api/v1/peers`, mirroring relay health's `last_health_status`/`last_health_error`.

## 2. Out of scope (C3)

- Controller active-probing of the translator (locked out — no path to the TUN). **Known gap:** the self-report is the egress's own claim — a split-brain where the Tayga child process is alive but translation is actually broken (wedged process, corrupted routes) reports healthy and is not demoted. End-to-end translation health would need a data-plane probe (a future phase).
- ECMP / load-balancing across multiple active egresses.
- Per-source or geo-aware egress affinity (always the single lowest-UUID eligible).
- A Web UI for egress health — C3 ships the API field; the UI consumes it later.
- Explicit hysteresis **columns** — v1 relies on threshold + sampling damping (§7); a min-dwell column is a documented follow-up if production shows thrashing.

## 3. Health model

Two independent signals, AND-combined:

1. **Self-report (translator liveness):** the egress CLI reports whether its
   Tayga translator is actually up — `running` (Up converged) AND the
   process supervisor's child is currently alive (not crash-looping in
   backoff). Carried as `nat64EgressHealthy *bool` on the REST heartbeat.
   - `nil` (field absent): a pre-C3 CLI, or a non-egress peer → the
     controller does NOT touch the health columns (judges by staleness
     only). This is what keeps C1/C2 working during a mixed-version rollout.
   - `true`: translator up → controller writes `health_status='healthy'`,
     `health_reason=''`.
   - `false`: translator down → `health_status='unhealthy'`,
     `health_reason='translator down'`.
2. **Staleness (peer reachability):** the controller's existing
   `peers.last_seen_at` (advanced by every heartbeat). An egress whose
   last heartbeat is older than `nat64EgressStaleAfter` (90s ≈ 3×
   `HeartbeatInterval`) is treated as unhealthy regardless of its last
   self-report — this catches a hard host crash that sends no final "down"
   heartbeat.

**Effective eligibility** is computed by ONE shared predicate used by both
the register path and the reaper, so the two can never disagree:

```go
// isEgressEligible reports whether p may be the active NAT64 egress right
// now. health_status comes from the column (self-report + reaper writes);
// freshness is derived LIVE from last_seen_at so the register path never
// routes to an egress that died inside the reaper's 30s gap.
func isEgressEligible(p *repo.Peer, now time.Time) bool {
    if !p.NAT64EgressApproved || p.ApprovalStatus != "approved" || p.Status == "disabled" {
        return false
    }
    if p.NAT64EgressHealthStatus == "unhealthy" { // NULL/'unknown'/'healthy' all pass
        return false
    }
    if p.LastSeenAt == nil || now.Sub(*p.LastSeenAt) > nat64EgressStaleAfter {
        return false
    }
    return true
}
```

`nat64EgressStaleAfter = 90 * time.Second` — a named constant carrying a
comment that it is 3× the client `HeartbeatInterval` (30s), duplicated in
the controller rather than imported because the CLI interval lives in a
separate module.

## 4. Components & data flow

### 4.1 CLI self-report (PR 1 — producer)

- **`supervisor.Alive()`** (`clients/core/nat64egress/supervisor.go`): the
  supervisor gains a mutex-guarded `childUp bool` lifecycle flag — set
  `true` after a successful `startFn`, cleared on the child's `Wait`
  return and on a start failure. `Alive()` reads it. This distinguishes
  "translating" from "crash-looping in backoff" (where `linuxManager.running`
  stays true but no child is up). Unit-tested via the fake `runnable`
  driving crash→backoff→restart.
- **`Manager.Healthy() bool`** (interface + both impls): linux returns
  `running && sup != nil && sup.Alive()`; the non-Linux noop returns false.
- **`Reconciler.ActiveHealth() *bool`**: under its mutex, returns `nil`
  when the peer is not the active egress (`!applied || !lastActive` — i.e.
  the controller never told it to translate), else a pointer to
  `mgr.Healthy()`. So a non-egress box reports nothing.
- **REST heartbeat**: `restHeartbeatRequest` gains
  `NAT64EgressHealthy *bool json:"nat64EgressHealthy,omitempty"`;
  `HeartbeatArgs` + `RunHeartbeat` carry it via a new nil-safe reporter
  `reportNAT64Health func() *bool`, wired in `up.go` from
  `egress.ActiveHealth()`. A pre-C3 controller silently drops the unknown
  JSON key → PR 1 is behavior-neutral on the controller.

### 4.2 Controller persistence + health-aware selection (PR 2)

- **Migration 00020** (mirrors 00015 relay-health style): nullable
  `nat64_egress_health_status TEXT` + `nat64_egress_health_reason TEXT`
  on `peers`, with `CHECK (nat64_egress_health_status IS NULL OR IN
  ('unknown','healthy','unhealthy'))`. Reversible goose Down (DROP
  CONSTRAINT IF EXISTS + DROP COLUMN IF EXISTS).
- **Heartbeat side-channel** (`apiPeersHeartbeat`): `peerHeartbeatRequest`
  gains `NAT64EgressHealthy *bool`. After `coord.Heartbeat`, when the
  pointer is non-nil, persist via a dedicated `peers.SetNAT64EgressHealth(
  ctx, id, reported bool)` (ConnectionPath pattern — outside the mesh-state
  contract). `nil` → skip (column unchanged). The repo method maps the
  bool to (`healthy`,``) / (`unhealthy`,`translator down`).
- **Shared predicate + selection**: `isEgressEligible` (§3) lives where
  both `computeNAT64EgressRoute` and the PR-3 reaper can call it.
  `computeNAT64EgressRoute` replaces its inline filter with
  `isEgressEligible(p, now)` and keeps the lowest-UUID tie-break. The
  register path therefore already skips a confirmed-unhealthy or stale
  egress — failover happens whenever an affected peer next re-registers
  (heartbeat-backstop or any policy change), even before the reaper exists.
- **API exposure**: `apiPeerJSON`/`peerToJSON` gain
  `nat64EgressHealthStatus` + `nat64EgressHealthReason`, normalising
  `''→'unknown'` at the JSON edge (relay-health precedent). Pinned by an
  `api_peers_test.go` wire-shape assertion.
- **Real-Postgres e2e**: register an approved egress, heartbeat it
  `nat64EgressHealthy:false`, assert the REST peer JSON shows
  `unhealthy`/`translator down` and that a second approved (healthy) egress
  is the one selected for the `/96` route.

### 4.3 Controller reaper — prompt failover (PR 3)

- **`StartNAT64EgressHealthReaper`** (mirrors `StartRelayHealthReaper`):
  an immediate sweep on startup, then a 30s ticker. Wired in `http.go`
  next to `StartRelayHealthReaper`.
- **Each sweep**, per tenant with ≥1 approved egress:
  1. **Staleness leg:** any approved egress whose `last_seen_at` is older
     than `nat64EgressStaleAfter` and whose status isn't already
     `unhealthy` → write `unhealthy`/`stale` (dedicated repo call). This
     is the only NEW status writer besides the heartbeat; there is no
     outbound probe (unlike relay health).
  2. **Selection-change → bump:** recompute the selected egress
     (`isEgressEligible` lowest-UUID) for the tenant. The reaper holds an
     **in-memory `lastSelected map[tenantID]uuid`**. If the freshly-computed
     selection differs from `lastSelected[tenant]`, call
     `h.coord.BumpPolicyRevision(tenant)` (the existing primitive: DB bump +
     PolicyChanged publish → affected peers re-register → recompute route)
     and update the map. **At most one bump per tenant per sweep.**
     - **Restart seeding:** on the first sweep after a controller restart
       (`lastSelected[tenant]` unset), bump once per tenant that has an
       approved egress, then seed the map — guaranteeing routes match
       current health even if an egress died during the restart. Bounded
       cost (number of NAT64 tenants), documented.
- **GLOBAL before/after, not per-row:** the bump decision is "did the
  tenant's *selected* egress change", computed over the whole approved set
  — NOT relay's per-row `relayEligibilityChanged` (which is only sound
  because relays are served as a set). A per-row check carries no
  information about whether the flipping peer was the active one.

## 5. Convergence & timing

**Graceful (translator process dies, host alive):** the supervisor sees
the child exit → `Alive()=false` → next heartbeat (≤30s) reports
`healthy:false` → controller writes `unhealthy` → next reaper tick (≤30s)
detects the selection change → bump → affected peers re-register. Typical
failover ≈ one heartbeat + one reaper tick (≤~60s).

**Hard crash (host gone, no final heartbeat):** only staleness fires —
`last_seen_at` crosses `nat64EgressStaleAfter` at t+90s → next reaper tick
(≤30s) → bump → re-register (≤30s heartbeat backstop for unsubscribed
peers). **Worst-case black-hole ≈ 90s + 30s + 30s ≈ 150s.** This is the
true crash-failover bound (the ~90s staleness alone is NOT the failover
time); it is an accepted bound for a hardening feature, signed off in this
spec. The graceful self-report path is a best-effort accelerator, not a
guarantee.

## 6. Error handling

- **Reaper DB error** on a sweep: log + skip that tenant/peer this tick
  (mirrors relay reaper's per-error `slog.Warn`); the next tick retries.
- **`BumpPolicyRevision` failure:** logged; `lastSelected` is NOT updated
  so the next sweep retries the bump (self-healing).
- **Heartbeat persist failure** (`SetNAT64EgressHealth`): logged at the
  API edge like the ConnectionPath side-channel; the heartbeat itself
  still succeeds (health is not part of the mesh-state contract).
- **No eligible egress** (all approved egresses unhealthy/stale, or none
  approved): `computeNAT64EgressRoute` returns `egressID = uuid.Nil` →
  no `/96` route emitted → synthesized v6 traffic black-holes (same
  surface as "no approved egress" today). Admins see all egresses
  `unhealthy` in the API.

## 7. Hysteresis / flap damping (v1 decision)

v1 ships **no explicit hysteresis column**; flap damping comes from the
signal design:

- **Demotion** is effectively damped by the 90s staleness window (3×
  heartbeat): a single dropped or late heartbeat does NOT demote (last_seen
  stays fresh). Only a sustained gap or an explicit `healthy:false` demotes.
- The realistic flap source — a crash-looping translator — is **sampled at
  the 30s heartbeat**, not continuously. The supervisor's 1s backoff means
  a persistently-broken translator reports steadily `false`, not flapping;
  a translator that recovers reports steadily `true`. The 30s sample of a
  fast loop reads as steady-unhealthy.

This is a conscious trade-off, not an oversight: the 5-lens panel flagged
promotion hysteresis; v1 addresses it via threshold + sampling rather than
a min-dwell counter. **If production shows a low-UUID egress thrashing the
active slot** (repeated tenant-wide re-registers), the clean follow-up is a
`nat64_egress_health_changed_at` timestamp + a promotion min-dwell in
`isEgressEligible` — an additive change requiring no rework of this design.

## 8. Testing

**Unit (CLI, in `clients/core`):**
- `supervisor.Alive()` across the lifecycle: false before start, true after
  a successful start, false after the child's Wait returns, true again
  after restart, false after Stop — via the fake `runnable`.
- `Manager.Healthy()` (linux build-tagged compile + the noop returning
  false); `Reconciler.ActiveHealth()` returns nil when never-active / down,
  non-nil bool when active.

**Unit (controller, pure):**
- `isEgressEligible`: a table over (approved, approval_status, status,
  health_status ∈ {NULL,unknown,healthy,unhealthy}, last_seen fresh/stale)
  → eligible/not. Confirms unknown=eligible, unhealthy=skip, stale=skip.
- `computeNAT64EgressRoute` health-aware: active dies → next-UUID picked;
  all unhealthy → uuid.Nil (no route); unknown egress still picked.
- Reaper selection-change: active dies → bump; non-active dies → no bump;
  recovered lower-UUID → bump; steady sweep → no bump; ≤1 bump/tenant/sweep;
  first-sweep-after-restart seeds (+ the one restart bump).

**Integration (controller, real Postgres):**
- The §4.2 register + heartbeat-false + select-the-healthy-egress e2e.
- Migration 00020 up/down against a throwaway Postgres before push.

**Manual (hardware, user's machines):** two approved egresses on real
Linux; kill the active one's tayga (or the host); confirm a non-egress
peer's `64:ff9b::<v4>` traffic fails over to the second egress within the
§5 bound (tcpdump on the new egress's WAN); confirm the API shows the dead
one `unhealthy`.

## 9. PR breakdown (3)

1. **PR 1 — CLI self-report (producer).** `supervisor.Alive()` +
   `Manager.Healthy()` + `Reconciler.ActiveHealth()` + the REST heartbeat
   `nat64EgressHealthy *bool` field + reporter wiring. Unit-tested; the
   controller ignores the field, so PR 1 is behavior-neutral.
2. **PR 2 — controller persistence + health-aware selection.** Migration
   00020 (2 columns) + heartbeat side-channel persistence + shared
   `isEgressEligible` + `computeNAT64EgressRoute` health filter (live
   staleness) + `/api/v1/peers` exposure. Real-Postgres e2e. After PR 2 a
   re-registering peer already avoids a confirmed-unhealthy/stale egress.
3. **PR 3 — reaper (prompt failover).** `StartNAT64EgressHealthReaper`
   (staleness sweep + global selection-change bump via
   `h.coord.BumpPolicyRevision`, in-memory `lastSelected`, restart seeding)
   + wiring in `http.go`. Unit-tested selection-change table. Makes failover
   prompt instead of waiting for an unrelated re-register; manual hardware E2E.

## 10. Phase boundary

C3 completes NAT64 hardening: the data path (C1→C2→C4) plus health-aware
single-active failover with admin visibility. Remaining future work, all
out of scope here: a data-plane translation probe (to close the
self-report split-brain gap), explicit promotion hysteresis if needed,
ECMP/multi-active, and the Web UI for egress health. With C3 merged + the
manual failover E2E green, an approved-egress death no longer black-holes
NAT64 indefinitely.
