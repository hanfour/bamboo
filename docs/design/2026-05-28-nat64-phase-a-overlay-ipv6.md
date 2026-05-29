# NAT64 Phase A — overlay IPv6 foundation (2026-05-28)

First implementation phase of the NAT64 / IPv6 dual-stack umbrella
(`docs/design/2026-05-27-nat64-architecture.md`). Gives every peer a
deterministic IPv6 ULA alongside its existing IPv4, and plumbs the
second family through schema, IPAM, the ACL compiler, all client
tunnel configs, and the admin/Web display surfaces. No DNS64
(Phase B), no translator (Phase C), no IPv6-only onboarding (umbrella
out of scope).

## 1. Scope

Each peer holds `100.x/32` (unchanged) **and** a `<ULA>/128` derived
deterministically from its IPv4. The phase ends when two peers can
ping each other on both families simultaneously over the mesh, the
controller exposes the v6 address on `/api/v1/peers`, and the Apple /
CLI / Web surfaces display it.

This phase also lands the three Phase C schema hooks (umbrella §5) so
the translator work does not force a second `peers` migration later.
Those columns exist but stay `false` / NULL until Phase C.

## 2. Cross-phase decisions inherited (locked in umbrella)

- ULA prefix `fdba:1100::/48`; Phase A uses a single shared `/64`
  (`fdba:1100::/64`) as the per-tenant default, mirroring the existing
  shared-`100.127.0.0/24` v4 model. Cross-tenant isolation is by the
  `(tenant_id, ip)` unique key, not by distinct CIDRs — so Phase A
  does **not** implement the umbrella's forward-looking 16-bit
  per-tenant `/64` slotting. That stays available for a future phase.
- v4 ↔ v6 reverse mapping: the low 32 bits of the v6 literally embed
  the IPv4 address (§4 below pins the exact encoding).
- ACL semantics: one HCL rule covers both families, resolved by peer
  identity (§6 below).

## 3. Schema migration — `00018_peer_ipv6.sql`

Goose SQL migration (the runner is `goose.SetBaseFS` over a
`//go:embed *.sql` FS — SQL-only, no Go migrations). All new columns
default to safe values so the migration is non-breaking, matching the
`00011_subnet_routes_exit_node.sql` precedent.

```sql
-- +goose Up
-- +goose StatementBegin

ALTER TABLE tenants ADD COLUMN ip6_pool CIDR NOT NULL DEFAULT 'fdba:1100::/64';
ALTER TABLE peers   ADD COLUMN ip6 INET;   -- nullable until backfill below

-- Phase C hooks (umbrella §5) — present now, exercised in Phase C.
ALTER TABLE peers   ADD COLUMN nat64_egress_capable  BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE peers   ADD COLUMN nat64_egress_approved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tenants ADD COLUMN nat64_prefix          TEXT;   -- NULL = well-known 64:ff9b::/96

-- Eager backfill (umbrella decision): derive each existing peer's v6
-- from its v4 by adding the v4's 32-bit integer offset to the tenant's
-- v6 pool network address. PostgreSQL inet arithmetic gives this
-- directly: (inet - inet) -> bigint, (inet + bigint) -> inet.
--   100.64.0.5 offset = 0x64400005 ; fdba:1100:: + that = fdba:1100::6440:5
-- The /64 pool guarantees host bits 32..63 are zero, so the v4 offset
-- lands cleanly in the low 32 bits with no carry into the prefix.
UPDATE peers p
SET ip6 = (host(t.ip6_pool)::inet + (p.ip - '0.0.0.0'::inet))::inet
FROM tenants t
WHERE p.tenant_id = t.id;

ALTER TABLE peers ALTER COLUMN ip6 SET NOT NULL;
ALTER TABLE peers ADD CONSTRAINT peers_tenant_ip6_unique UNIQUE (tenant_id, ip6);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE peers   DROP CONSTRAINT IF EXISTS peers_tenant_ip6_unique;
ALTER TABLE tenants DROP COLUMN IF EXISTS nat64_prefix;
ALTER TABLE peers   DROP COLUMN IF EXISTS nat64_egress_approved;
ALTER TABLE peers   DROP COLUMN IF EXISTS nat64_egress_capable;
ALTER TABLE peers   DROP COLUMN IF EXISTS ip6;
ALTER TABLE tenants DROP COLUMN IF EXISTS ip6_pool;

-- +goose StatementEnd
```

**Backfill / runtime parity invariant:** the SQL backfill above and
the Go `v6map.V6From()` helper (§4) MUST produce bit-identical results.
A test asserts this for a representative v4 set (§9). If the encoding
ever changes, both sides change together.

## 4. IPAM encoding — `apps/controller/internal/ipalloc/v6map/`

New package so `ipalloc` itself stays single-family and untouched.

```go
// V6From maps an IPv4 address into the low 32 bits of the v6 pool.
// pool must be a /64; its host portion (low 64 bits) is zero by
// definition of a network address, so the 32-bit v4 lands in bits
// 96..127 with no carry into the prefix.
func V6From(pool netip.Prefix, v4 netip.Addr) netip.Addr

// V4From extracts the embedded IPv4 from a mapped v6 address.
// ok is false if v6 is not inside a recognisable mapped range
// (host bits 32..63 nonzero, or not v4-in-low-32 shaped).
func V4From(v6 netip.Addr) (v4 netip.Addr, ok bool)
```

Encoding (the one locked decision this phase makes concrete):

```
pool   = fdba:1100::/64
v4     = 100.64.0.5         bytes = 64 40 00 05
result = fdba:1100::6440:5  (low 32 bits = the four v4 bytes)
```

`V6From` takes `pool.Addr().As16()`, overwrites bytes 12..15 with
`v4.As4()`, returns `netip.AddrFrom16`. This is exactly equivalent to
the SQL `+ offset` form because the pool's bytes 8..11 are zero.

## 5. IPAM allocation + register flow

New dual wrapper alongside the existing `NextFree`:

```go
func NextFreeDual(v4Pool, v6Pool string, usedV4 []string) (v4, v6 string, err error) {
    v4, err = NextFree(v4Pool, usedV4)
    if err != nil {
        return "", "", err
    }
    v6 = v6map.V6From(netip.MustParsePrefix(v6Pool), netip.MustParseAddr(v4)).String()
    return v4, v6, nil
}
```

No independent v6 collision scan: v6 is a pure function of v4, so the
existing `peers_tenant_ip_unique` (v4) guarantees `peers_tenant_ip6_unique`
holds automatically.

`apps/controller/internal/handlers/coordinator.go:342` changes from the
single `ipalloc.NextFree(tenant.IPPool, used)` call to `NextFreeDual`,
persists both `ip` and `ip6`, and returns both in the register
response. The `/api/v1/peers` payload (`api_peers.go`) gains a
top-level-per-peer `ip6` string field.

## 6. ACL compiler dual-family emit

`apps/controller/internal/server/api.go:2551`:

```go
// before
edge.AllowedIPs = []string{dst.IP + dstPrefixSuffix(dst.IP)}

// after
edge.AllowedIPs = []string{
    dst.IP  + "/32",
    dst.IP6 + "/128",
}
```

One ACL rule emits both families (umbrella §4.4). HCL grammar is
unchanged; IPv6 literal support in policy text is deferred to Phase D.
The same dual emit is applied wherever `AllowedIPs` is rendered — the
coordinator enforcement path and the `/simulate` preview path
(`api.go:2578` region) must stay consistent so the simulator matches
what clients actually receive.

## 7. Client tunnel interface dual-stack

| Platform | Change | File |
|---|---|---|
| CLI (Linux wg-go) | `wg setconf` AllowedIPs list already accepts mixed families; add `ip -6 addr add <v6>/128 dev wg0` next to the existing v4 interface address bring-up | `clients/cli/internal/sync/daemon.go` |
| Apple (System Extension PacketTunnel, post-#153) | set `NEPacketTunnelNetworkSettings.ipv6Settings = NEIPv6Settings(addresses:[v6], networkPrefixLengths:[128])` alongside the existing `ipv4Settings`; included routes mirror the v4 reachability set | `clients/apple/PacketTunnel/PacketTunnelProvider.swift` |
| Web (display only) | `PeerTable.tsx` renders the v6 as a muted mono sub-line beneath the existing IP cell, hidden on narrow viewports (same `hidden lg:*` pattern as the Client-ver column from PR #227) | `apps/web/src/components/PeerTable.tsx` |

## 8. MagicDNS wire-up

`clients/apple/Shared/ConnectionViewModel.swift:987` changes the
`ipv6: nil` it passes into `MagicDNSPeerStore.PeerEntry` to
`ipv6: peer.ip6`. `MagicDNSResolver` already returns a correct AAAA
record for a non-nil `ipv6` and NOERROR+empty otherwise
(`MagicDNSResolver.swift:96`) — no resolver change needed. This was
foreseen: `MagicDNSPeerStore` has carried the reserved `ipv6: String?`
field since the MagicDNS data-plane work.

## 9. Error / edge cases

- **v6 pool exhaustion** — impossible while v4 is not exhausted, since
  v6 is a pure function of v4 and a `/64` (2^64) dwarfs a `/24` (254
  usable). Only the existing v4-pool-usage gauge is needed; no v6 gauge.
- **Admin sets `ip6_pool` to a non-`/64`** — rejected at API edge:
  must be exactly `/64` so the 32-bit v4 offset lands in bits 96..127
  with no carry into the prefix.
- **v4 renumber of an existing peer** — v6 is NOT recomputed in place;
  the stored `ip6` is pinned for the peer's lifetime. A renumber is a
  re-register, which runs `NextFreeDual` and writes a fresh pair.
- **Backfill vs runtime divergence** — guarded by the parity test (§3,
  §4). The single source of truth for the encoding is the documented
  "v4 bytes in low 32 bits" rule.

## 10. Test plan

- `ipalloc/v6map/v6map_test.go` — `V6From` / `V4From` round-trip across
  a representative v4 set incl. boundaries (`x.x.x.0`, `x.x.x.255`).
- **Backfill parity test** — assert the SQL `inet + offset` form and
  `v6map.V6From` agree for the same v4 set (table-driven; can run the
  SQL form against a test DB or replicate the arithmetic in the test).
- `ipalloc/ipalloc_test.go` — `NextFreeDual` returns a correctly paired
  v4/v6 and surfaces `ErrPoolExhausted` from the v4 layer.
- migration test — after up, no peer has NULL `ip6`; the unique
  constraint holds; down cleanly drops all six columns/constraints.
- `api_peers_test.go` — register response and `/api/v1/peers` JSON both
  carry `ip6`.
- ACL compiler test — a single rule yields both `…/32` and `…/128` in
  `AllowedIPs`, on both the enforce and simulate paths.
- Apple `MagicDNSPeerStoreTests.swift` — a peer with a v6 returns the
  expected AAAA; a peer without still returns NOERROR+empty.
- E2E (manual, mac-mini): two peers ping each other on v4 and v6
  simultaneously; confirm the System Extension tunnel carries both.

## 11. PR breakdown (≈3)

1. **Schema + IPAM + register** — migration `00018`, `v6map` package,
   `NextFreeDual`, `coordinator.go` register dual-write, `/api/v1/peers`
   `ip6` field, backend + parity tests.
2. **ACL dual-emit + Web display** — `api.go` enforce/simulate dual
   emit, `PeerTable.tsx` v6 sub-line, controller tests.
3. **Client tunnels + MagicDNS wire-up** — CLI `ip -6 addr add`, Apple
   `PacketTunnelProvider` `ipv6Settings`, the `ConnectionViewModel`
   one-line MagicDNS fix, E2E verification.

## 12. Out of scope for Phase A

- DNS64 synthesis (Phase B).
- Any 6→4 translation (Phase C).
- ACL HCL accepting IPv6 literals (Phase D).
- IPv6-only client onboarding (umbrella out of scope).
- Per-tenant `/64` slotting (deferred; shared default `/64` for now).

## 13. Side finding (not Phase A scope — file separately)

Five call sites pass the literal `"100.64.0.0/24"` to
`tenants.GetOrCreate` as the default pool, inconsistent with the
`100.127.0.0/24` migration default set in PR #229:
`server/http.go:822`, `handlers/coordinator.go:652`,
`handlers/auth.go:112`, `handlers/policy.go:351`,
`handlers/telemetry.go:51`. Existing tenants read the DB value so they
are unaffected, but the literal is stale. Recommend a standalone
cleanup PR; **do not** bundle into Phase A.
