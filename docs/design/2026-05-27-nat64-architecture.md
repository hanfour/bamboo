# NAT64 / IPv6 dual-stack — umbrella architecture (2026-05-27)

Umbrella spec for the §4 P2 NAT64 backlog item from
`docs/design/2026-05-17-feature-gap-roadmap.md` (line 122). This doc
locks the four-phase decomposition, the cross-phase decisions, and
the translator-location ADR. Each phase will have its own dated spec
under `docs/design/`; this doc serves as the index and the design
record for the cross-cutting choices.

## 1. Goal

Allow IPv6-only hosts on the mesh to reach IPv4-only services — both
external public-internet IPv4 destinations and other mesh peers that
hold only `100.x/24` addresses — without disturbing the zero-config
experience of existing pure-IPv4 hosts. Long-term direction tracks
Tailscale's 4via6 + DNS64 model but is not required to be bit-compatible.

## 2. Out of scope

Explicitly **not** addressed in this umbrella or any of its four phases:

- **IPv6-only client onboarding** — i.e. making the controller and
  relays reachable from an IPv6-only carrier (T-Mobile, Apple Private
  Relay, JP IPv6-only mobile). Separate spec when Apple App Review or
  a paying customer forces the issue.
- **WireGuard underlay over IPv6** — peer-to-peer handshakes carried
  on `[v6]:port` endpoints to dodge a NAT layer. Independent sprint.
- **Per-family billing / quota split** — NAT64-translated bytes are
  not separately accounted from native IPv4 bytes for tenant billing.

## 3. Phase decomposition

Strict sequential dependency — each phase rests on the previous one's
output (an IPv6 address with nowhere to route packets is useless; a
DNS64 synthesis with no translator is a black hole).

| Phase | Scope | Depends on | PR estimate |
|---|---|---|---|
| **A. Overlay IPv6 foundation** | Each peer is dual-addressed with `100.x/32` + `<ULA>/128`; controller IPAM issues both; WireGuard `AllowedIPs` carries both families; ACL semantics unified per peer-identity; Apple / CLI / Web surface both addresses. | — | 3–4 |
| **B. DNS64 in MagicDNS** | Resolver synthesises AAAA from A when no native AAAA exists, using the configured NAT64 prefix; per-tenant prefix override; Apple `NEDNSProxyProvider` and Linux paths kept in lockstep. | A | 2–3 |
| **C. NAT64 translator data plane** | The actual 6→4 packet rewrite. Translator runs on admin-designated egress peers (see §5 ADR). Includes route advertisement, health probing, ACL synthesis. | A, B | 3–5 |
| **D. ACL / policy / observability** | HCL grammar accepts IPv6 literals; route conflict checker is dual-stack-aware; audit log + Prometheus metrics carry a `nat64_translated` label. | C | 2 |

Total: ~10–14 PRs across 3–4 sprints.

## 4. Cross-phase decisions (locked here)

### 4.1 ULA prefix

Use an RFC 4193 randomly-generated `/48` rooted at `fdba:1100::/48`. The
last 32 bits embed the existing IPv4 (see §4.3). The `/48` size leaves
16 bits for per-tenant `/64` subnetting later without renumbering.

**Not chosen:** Tailscale's `fd7a:115c:a1e0:ab12::/64`. Collision risk
when a host runs both Tailscale and bamboo, which is a real scenario
already addressed once via the `100.64 → 100.127` move in PR #229.
Pick a different ULA up-front for the same reason.

### 4.2 NAT64 prefix

Default to the well-known `64:ff9b::/96` (RFC 6052). Admin override
per-tenant via `tenants.nat64_prefix TEXT` allows network-specific
prefixes (NSPs) when a tenant has site policy that conflicts with the
well-known. Validation: must be `/96` and well-formed; reject anything
else at API edge.

### 4.3 IPAM — v4 ↔ v6 reverse mapping

The `/128` is derived deterministically from the `/32` by embedding
the 32-bit IPv4 address into the low 32 bits of the v6 address. For
`100.64.0.5` (bytes `64 40 00 05`):

```
fdba:1100::6440:5     ↔     100.64.0.5
fdba:1100::6440:1f4   ↔     100.64.1.244
```

Per-tenant subnetting uses bits 48–63 of the prefix (the `/48`'s
sub-`/64` slot), so tenant 7 lives in `fdba:1100:7::/64` etc.

The encoding principle is **deterministic, reversible,
log-greppable** — one `grep '6440:5'` should hit both the v4 and v6
addresses of the same peer in the same log line. Phase A spec pins
the exact byte order and the per-tenant slot bit layout; this section
only locks the shape (IPv4 in the low 32 bits, tenant index in the
sub-`/64`).

**Trade-off:** an attacker who sees a v6 address can infer the v4. This
is acceptable because the v4 is already pushed via WireGuard `AllowedIPs`
to every peer in the same ACL group; the v6 leaks nothing new. The
debug / on-call benefit (one `grep` matches both families) is large.

### 4.4 ACL semantics

A single HCL rule `src = "tag:dev"` `dst = "tag:prod:443"` applies to
both `src.ipv4` → `dst.ipv4` and `src.ipv6` → `dst.ipv6`. Resolution is
by **peer identity**, not by literal address. The compiler emits both
families into `AllowedIPs`. Cross-family rules (v6 src → v4 dst literal)
are handled by the NAT64 synthesis path, not by extra ACL grammar.

## 5. Translator location ADR — D2 (admin-designated egress peer)

**Decision:** the NAT64 translator runs on admin-designated egress
peers (Linux), using Jool or Tayga. This mirrors the existing subnet
router (#149) and exit node (#149) admin model — the third instance
of the "admin flags a peer to do something special for the tenant"
pattern.

### Options considered

**D1 — centralised translator container** alongside the controller.
- ✅ Lowest implementation cost (deploy Jool sidecar, route to it).
- ✅ Single metrics / config point.
- ❌ Single point of failure for all 6→4 flows.
- ❌ Cross-region latency: forces all translated traffic through
  controller region.
- ❌ Controller operator sees plaintext of translated flows; violates
  the P2P / zero-trust brand promise.
- ❌ Controller bandwidth bill for translated traffic.
- **Rejected** primarily on the P2P violation and SPOF grounds.

**D2 — admin-designated egress peer** (chosen).
- ✅ Reuses the subnet-router / exit-node admin UX shipped in #149 —
  same migration shape, same Web UI pattern, same admin mental model.
- ✅ Translation happens on tenant-owned hardware; controller never
  sees translated bytes.
- ✅ Admin places egress peer close to actual IPv4 services (typically
  the same LAN); cross-region latency is the admin's choice, not the
  architecture's tax.
- ✅ Multiple egress peers → redundancy via WireGuard `AllowedIPs`
  routing + health probe.
- ✅ Cross-platform burden is **zero** for Apple / iOS / Windows —
  those clients only need DNS64 synthesis (Phase B) and dual-stack
  routing (Phase A). Translator code lives only on Linux egress peers.
- ⚠️ Egress peer death blackholes flows routed through it until
  health probe re-routes. Mitigated by multiple egresses; documented
  as expected behaviour.
- ⚠️ Adds one new admin flag and one new approval flow.

**D3 — userspace translator embedded in every client** (clatd-style).
- ✅ True P2P, no SPOF, no extra hop.
- ❌ Four platforms × non-trivial userspace code: Linux
  (wireguard-go fork), macOS + iOS (WireGuardKit fork), Windows
  (wireguard-windows fork). WireGuardKit is upstream-maintained; a
  fork that intercepts pre-encryption packets to rewrite headers
  diverges hard from upstream.
- ❌ Apple PacketTunnelProvider is a System Extension as of PR #153;
  injecting a translator inside the sandbox is at least as painful as
  the NEDNSProxyProvider work in PRs #126–#129.
- ❌ Maintenance burden compounds every upstream WireGuard release.
- **Rejected** on platform-fork maintenance cost. Small-team
  pragmatism — see `[[feedback-self-reliant-over-upstream]]` — argues
  against carrying four big patch sets indefinitely.

### Hooks Phase A must reserve for D2

Phase A's migration must include these columns up-front to avoid a
later migration round-trip. Implementation is deferred to Phase C; the
columns exist but read as `false` / NULL until then.

- `peers.nat64_egress_capable BOOL DEFAULT false` — client self-reports
  the box can translate (Linux with Jool/Tayga binary present).
- `peers.nat64_egress_approved BOOL DEFAULT false` — admin approval,
  parallel to `exit_node_approved`.
- `tenants.nat64_prefix TEXT` — see §4.2.

Phase A's route conflict checker (`apps/controller/internal/routes/conflicts.go`)
must learn that `<nat64_prefix>::/96 → egress peer` is a synthesised
route to avoid false-positive conflicts later.

## 6. Document conventions

- **This umbrella** locks direction only; no implementation detail.
- **Each phase** gets its own spec at `docs/design/YYYY-MM-DD-nat64-phase-{A,B,C,D}-<topic>.md`.
- **PR-list back-fill**: once a phase ships, replace the "PR estimate"
  cell in the §3 table with the actual PR numbers, in the style of
  `docs/design/2026-05-17-feature-gap-roadmap.md`.
- **ADR changes**: if the D2 decision is revisited mid-execution, do
  it as an amendment section at the bottom of this doc, not by
  rewriting §5. Preserve the rejected-option rationale.

## 7. Next step

Brainstorm and write the Phase A spec
(`docs/design/YYYY-MM-DD-nat64-phase-a-overlay-ipv6.md`) covering ULA
prefix activation, IPAM encoding precise format, migration shape,
ACL compiler dual-stack pass, WireGuard `AllowedIPs` dual-family
emit, and the cross-platform tunnel interface configuration for
Apple / CLI / Web display.
