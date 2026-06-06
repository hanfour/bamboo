# NAT64 hardening — RFC 6147 §5.1.4 special-use-IPv4 exclusion (2026-06-06)

A post-completion hardening of the NAT64 Apple DNS64 client (C4). The DNS
proxy currently synthesises an AAAA for **every** A record of an external
IPv4-only name. This change stops synthesising for A records whose IPv4 is
a special-use / non-globally-routable address, per RFC 6052 §3.1 ("the
well-known prefix MUST NOT be used to translate non-global IPv4
addresses") and RFC 6147 §5.1.4 (the DNS64 exclusion set).

Scope is the Apple client only (pure synthesis layer + a debug log in the
DNS proxy); no controller / proto / migration change.

## 1. Motivation — correctness AND security

Without the filter, an attacker-controlled or misconfigured external DNS
that returns an A record pointing at a private/local/mesh address makes
the client synthesise `<nat64_prefix>::<that-v4>`, route it to the
approved Linux egress (C1), where C2's Tayga translates it back to the
private v4 and MASQUERADEs it out the egress's **WAN onto the egress's own
LAN** — an SSRF-like reach. The verified worst case: a returned
`A 100.127.0.x` (CGN space, RFC 6598) — which is exactly the bamboo mesh's
own tunnel v4 range (default tenant CIDR `100.127.0.0/24`, inside
`100.64.0.0/10`) — would let the egress reach **another peer's tunnel
address**, bypassing the per-peer WireGuard `AllowedIps` ACL. The egress's
Tayga path applies no destination filter, so this client-side exclusion is
the chokepoint that denies the route at creation.

## 2. Architecture

A pure predicate plus a one-line filter, at the single point where v4s get
embedded into the prefix.

- **`NAT64Synth.isSpecialUseV4(_ v4: [UInt8]) -> Bool`** (new, in
  `clients/apple/Shared/NAT64Synth.swift` — Foundation-only, lives in the
  `AppleSharedTests` target). Returns true when `v4` falls in a special-use
  range that must not be synthesised. Guards `v4.count == 4` (returns false
  otherwise — matches `synthesize`'s defensive `precondition` style; callers
  pass 4-byte rdata from `aRecords`).
- **`DNSMessage.synthesizeResponse`** (`MagicDNSResolver.swift`) filters the
  parsed A-record v4s **before** the synthesise map:
  ```swift
  static func synthesizeResponse(aaaaQuery: DNSMessage, aResponse: Data, prefix: [UInt8]) -> Data? {
      guard let v4s = aRecords(aResponse) else { return nil }
      let synthesizable = v4s.filter { !NAT64Synth.isSpecialUseV4($0) }
      guard !synthesizable.isEmpty else { return nil }   // all special-use → relay NODATA
      let v6s = synthesizable.map { NAT64Synth.synthesize(prefix: prefix, v4: $0) }
      return aaaaRecords(for: aaaaQuery, ipv6s: v6s)
  }
  ```
  In a **mixed** response (one private + one public A) only the public
  address is synthesised — the private one is silently dropped, never
  embedded (defeats a DNS-rebinding-style "pad a public A to keep synthesis
  alive while injecting a private A" attack). When **every** A is
  special-use, `synthesizable` is empty → `nil` → the caller relays the
  original NODATA AAAA verbatim (the established no-synthesis path —
  `DNSProxyProvider.maybeDNS64` already does `writeToFlow(aaaaResp)` on nil).

- **`isSpecialUseV4` implementation — MUST be a mask-loop over a
  `(network: [UInt8], prefixLen: Int)` table, NOT per-octet conditionals.**
  The `/10` (100.64) and `/12` (172.16) masks are non-octet-aligned; a naive
  first-octet check (`v4[0] == 100`) is WRONG — it over-matches
  100.0–100.63 / 100.128–100.255 and under-protects the actual CGN/RFC1918
  block, re-opening the exact SSRF vector this hardening closes. The loop
  compares full prefix bytes with `==` and the final partial byte with
  `0xFF << (8 - bitsInLastByte)`, one code path for aligned and non-aligned
  masks alike.

## 3. The exclusion set

**EXCLUDE (do NOT synthesise)** — the RFC 6890 Global=False blocks + the
deprecated/non-unicast ranges that have no legitimate NAT64 destination use:

| Range | Why |
|---|---|
| `0.0.0.0/8` | "this host on this network" |
| `10.0.0.0/8` | RFC 1918 private |
| `100.64.0.0/10` | CGN (RFC 6598) — **covers the bamboo mesh's own `100.127.0.0/24`**; the security-critical entry. The full `/10`, not a tenant `/24`, so every present/future tenant CIDR is covered. |
| `127.0.0.0/8` | loopback |
| `169.254.0.0/16` | link-local |
| `172.16.0.0/12` | RFC 1918 private |
| `192.168.0.0/16` | RFC 1918 private |
| `192.88.99.0/24` | 6to4 relay anycast (RFC 3068, deprecated RFC 7526) |
| `198.18.0.0/15` | benchmarking (RFC 2544) — sometimes a local blackhole on real LANs |
| `224.0.0.0/4` | multicast — never a valid unicast A destination |
| `240.0.0.0/4` | reserved (includes `255.255.255.255` limited broadcast) |
| `192.0.0.0/24` **except** `192.0.0.170` and `192.0.0.171` | IETF Protocol Assignments (RFC 6890 Global=False) — DS-Lite `192.0.0.0/29`, dummy `192.0.0.8`, PCP anycast — are excluded; the two `ipv4only.arpa` addresses (RFC 7335) are **carved out as synthesisable** so the hardware-E2E runbook's synthesis smoke-test keeps working. |

**KEEP synthesisable** (the default — global unicast — plus two deliberate
carve-outs):
- Global unicast IPv4 (everything not in the table).
- `192.0.0.170` / `192.0.0.171` (`ipv4only.arpa`) — the runbook's
  "did synthesis happen" check (`docs/deployment/nat64-hardware-e2e.md` §2).
  *Note: synthesising `ipv4only.arpa` is a test affordance, not an RFC 7050
  necessity — the NAT64 prefix is controller-supplied here, not discovered.*
- TEST-NET-1/2/3 (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`) —
  left synthesisable: documentation ranges with ~zero security weight, and
  the runbook §2 uses `203.0.113.5.nip.io` as its worked reachable-target
  example. Excluding them would break that example for no security gain.

`isSpecialUseV4` carries a code comment citing the operative norms (RFC
6052 §3.1, RFC 6147 §5.1.4, IANA IPv4 Special-Purpose Registry / RFC 6890
Global=False) and pinning the `192.0.0.170/.171` carve-out to the runbook,
so a future "special-use sweep" doesn't silently break the documented test
or mis-edit the table.

## 4. Observability

The skip is otherwise invisible: `synthesizeResponse` returning `nil`
collapses "all A special-use", "no A records", and "upstream fail" into the
same relay-NODATA path, leaving an operator debugging "why isn't this
IPv4-only name working through NAT64?" with no signal.

In the provider's `maybeDNS64` nil branch (both `DNSProxy-macOS` and
`DNSProxy-iOS` — byte-identical), distinguish the special-use case using
the existing pure `aRecords`: when the A response **had** records but
`synthesizeResponse` returned nil, log at `os_log .debug`:
```
"dns64 skip: all %{public}d A records special-use for %{public}@"
```
— the query name `%{public}@` + the excluded count; **not** the address
values, and `.debug` only (per-query, high-volume, diagnostic). This
matches the provider's existing `.debug` logs (e.g. the upstream-timeout
log). No change to the pure layer's purity.

## 5. Testing

**Unit (pure, `AppleSharedTests` — `DNS64Tests.swift`, mirror the existing
per-range boundary style):**
- `isSpecialUseV4` inclusive boundaries of every excluded range, especially
  the non-octet-aligned masks (the regression guard against a naive
  first-octet rewrite):
  - `100.63.255.255` → false; `100.64.0.0` + `100.127.255.255` → true;
    `100.128.0.0` → false.
  - `172.15.255.255` → false; `172.16.0.0` + `172.31.255.255` → true;
    `172.32.0.0` → false.
  - `240.0.0.0` + `255.255.255.255` → true; `239.255.255.255` → false.
  - `0.0.0.0` → true; `1.0.0.0` → false.
  - `169.253.255.255` → false; `169.254.0.0`/`169.254.255.255` → true;
    `169.255.0.0` → false.
  - `192.88.99.0` / `192.88.99.255` → true; `192.88.98.255`/`192.88.100.0` → false.
  - `198.18.0.0` / `198.19.255.255` → true; `198.17.255.255`/`198.20.0.0` → false.
  - The `192.0.0.0/24` carve-out: `192.0.0.169` → true (excluded);
    `192.0.0.170` + `192.0.0.171` → false (synthesisable);
    `192.0.0.172` → true (excluded); `192.0.0.0` → true.
  - A sample global unicast (`93.184.216.34`, `8.8.8.8`, `203.0.113.5`) → false.
  - `v4.count != 4` → false (guard).
- `synthesizeResponse` filtering:
  - mixed `[93.184.216.34, 192.168.1.1]` → synthesises ONLY the public one
    (one AAAA answer, embedding `93.184.216.34`); the private one is absent.
  - all-private `[10.0.0.1, 192.168.0.1]` → nil (caller relays NODATA).
  - `ipv4only.arpa` v4 (`192.0.0.170`) → still synthesises (carve-out).

**Not unit-tested (NE provider):** the `os_log .debug` skip line —
compile-verified (`xcodebuild ... CODE_SIGNING_ALLOWED=NO` macOS + iOS),
and the byte-identical-providers diff gate (as in C4 PR3).

## 6. Out of scope / follow-up

- **Egress-side destination filter (defense in depth) — a SEPARATE hardening
  item, NOT this change.** The egress's Tayga path
  (`clients/core/nat64egress/nat64egress_linux.go`) translates any
  `<prefix>::<v4>` and MASQUERADEs it with no destination filter. This
  client-side exclusion is the only layer closing the SSRF, so a non-Apple
  client, an older un-upgraded client, or a future second synthesis path
  re-opens the full vector. The egress SHOULD drop translation to
  RFC1918/CGN/loopback/link-local destinations (an `ip6tables`/route filter
  on the `nat64` TUN, or a Tayga config). Recorded as a backlog hardening
  item; it complements, not replaces, this change.
- **Per-tenant destination allowlist** — if a tenant ever legitimately needs
  a private split-horizon target reachable via a specific egress, the
  sanctioned escape hatch is a controller-pushed per-tenant allowlist of
  permitted destination prefixes, NOT relaxing the global `isSpecialUseV4`
  filter. Not built now; recorded so nobody weakens the filter later.
- Controller/Go-side synthesis is unaffected (there is none on that path —
  synthesis is Apple-only).

## 7. Known intentional behavior change

A split-horizon name whose external (egress-side) DNS legitimately returns
a private `10.x` / `192.168.x` that the user wants reachable via the
egress's LAN is, by design, **no longer reachable via NAT64**. bamboo has
no per-name allowlist, and the same mechanism that would permit the benign
case permits the hostile `A 192.168.1.1` → egress-router SSRF. Breaking it
by default is the correct security posture; it is documented (runbook §7
troubleshooting row + the §4 debug log make it diagnosable), not silently
swallowed.

## 8. PR shape

A single small Apple-only PR:
- `NAT64Synth.isSpecialUseV4` (pure, mask-table) + the `synthesizeResponse`
  filter.
- The macOS + iOS provider `.debug` skip log (byte-identical, diff-gated).
- `DNS64Tests` boundary + filtering tests.
- A troubleshooting row in `docs/deployment/nat64-hardware-e2e.md` §7.
- CI-verifiable: `AppleSharedTests` green + macOS/iOS compile; no hardware
  E2E needed (the existing C4 §7 runbook check exercises it end-to-end on
  hardware, with `ipv4only.arpa` confirmed still synthesising via the carve-out).
