# NAT64 Phase C4 — Apple DNS64 client (external-name AAAA synthesis) (2026-06-03)

Fourth and final sub-project of NAT64 Phase C, and the deferred Phase B
PR 2. C1 (#238/#239) made an approved egress reachable (every peer routes
`<nat64_prefix>::/96` to it). C2 (#240/#241) made that egress actually
translate v6→v4 with Tayga. **C4 gives the translator a real consumer:**
the Apple client's DNS proxy synthesises AAAA records for external
IPv4-only names so that ordinary app traffic resolves to
`<nat64_prefix>::<v4>`, flows through the mesh to the egress, and is
translated out its WAN. Until C4, NAT64 is exercised only by manually
targeting `64:ff9b::<v4>`; after C4 it works end-to-end for real names.

## 1. Scope

- **Pure synthesis + DNS answer parsing** (`clients/apple/Shared`,
  unit-tested in `AppleSharedTests`): RFC 6052 §2.2 IPv4→IPv6 embedding
  matching the controller's Go `Synthesize`; a DNS answer-section parser
  to extract A records from an upstream response; a multi-AAAA response
  builder; and a pure DNS64 decision helper.
- **Config surfacing + decode + channel**: the Apple client registers over
  **REST**, whose `peerRegisterResponse` does not yet carry the NAT64
  config (only the gRPC `RegisterResponse` does). A small controller change
  surfaces `dns64Enabled` + `nat64Prefix` on the REST response (they are
  already on the in-handler proto `resp`); the Swift client decodes them
  and pushes them to the DNS proxy extension. This includes **fixing the
  currently-dormant iOS MagicDNS config channel** (a prerequisite — see §3).
- **Provider 2-stage upstream** (RFC 6147): refactor the DNS proxy's
  single-shot upstream forwarder into a reusable query primitive, and add
  the AAAA→(NODATA)→A→synthesise path to both the macOS and iOS providers.

This is **macOS + iOS**. RFC 6147 standard behaviour: a name with a real
AAAA passes through unchanged; only an AAAA NODATA triggers synthesis.

## 2. Out of scope (C4)

- Stateful per-flow tracking, EDNS, DNSSEC validation — the proxy relays
  these unchanged as it does today.
- RFC 6147 §5.1.4 special-use IPv4 exclusion (private / loopback /
  link-local ranges not synthesised) — v1 synthesises every A answer;
  the exclusion is a documented future refinement.
- The Linux/CLI DNS path — the CLI has no DNS interception (MagicDNS is
  Apple-only); a mesh Linux peer reaching NAT64 destinations still does so
  by targeting `64:ff9b::<v4>` directly (or via its own resolver config),
  outside C4.
- Health/observability of the synthesis path beyond `os_log` — no
  controller reporting (a later concern, cf. C3).

## 3. The iOS config-channel prerequisite

`MagicDNSPeerStore` hard-codes its storage at `/Users/Shared/dev.hanfour.bamboo`
for **all** platforms. That path exists on macOS (chosen deliberately —
the macOS DNS proxy is a *System Extension* running in a privilege context
that could not share an App Group container with the user-context app, per
issue #154). On **iOS the path is unreachable** from the sandbox, so the
iOS DNS extension's `store.peers()` always returns empty and **iOS
MagicDNS is effectively dormant today** — every `*.bamboo` query fails.

Both iOS targets (`BambooApp-iOS`, `DNSProxy-iOS`) already declare the App
Group `group.dev.hanfour.bamboo` in their entitlements; it is simply
unused by the store. Because the iOS DNS proxy is an *App Extension*
(runs in the host app's context, unlike macOS's System Extension), the App
Group container **does** work for it.

**C4 makes the store path platform-conditional:**

```swift
#if os(macOS)
// System Extension can't share the App Group container (issue #154);
// a world-readable /Users/Shared file is the macOS channel.
static let baseDir = "/Users/Shared/dev.hanfour.bamboo"
#else
// iOS App Extension shares the already-entitled App Group container.
static var baseDir: String {
    FileManager.default
        .containerURL(forSecurityApplicationGroupIdentifier: "group.dev.hanfour.bamboo")!
        .appendingPathComponent("dev.hanfour.bamboo").path
}
#endif
```

This single change revives iOS MagicDNS **and** gives C4 a working iOS
channel for the NAT64 config. It is delivered in PR 2.

## 4. Components

### 4.1 Pure synthesis + parsing (`Shared/`, in `AppleSharedTests`)

All additions are Foundation-only (no NetworkExtension) so they compile
into `AppleSharedTests` and are unit-tested. They live alongside the
existing `MagicDNSResolver` / `DNSMessage`.

- **`nat64Synthesize(prefix: [UInt8], v4: [UInt8]) -> [UInt8]`** — takes a
  16-byte /96 prefix and a 4-byte v4, returns the 16-byte synthesised
  address with the v4 in bytes 12–15 (RFC 6052 §2.2). Must match the
  controller's Go `apps/controller/internal/nat64.Synthesize` byte-for-byte;
  the golden test uses the same vector `93.184.216.34` → `64:ff9b::5db8:d822`.
- **`nat64PrefixBytes(_ s: String) -> [UInt8]?`** — parse a `<...>::/96`
  prefix string (from the register response) into the 16-byte base,
  validating it is a /96 (reuse / extend the existing `ipv6Bytes` helper;
  reject non-/96 and IPv4-mapped, mirroring the controller's `ParsePrefix`).
- **DNS answer-section parser** — extend `DNSMessage.parse` (or add a
  sibling) to decode the answer section so A (type 1) rdata can be read
  from an upstream response. Returns the list of 4-byte A rdata values and
  whether the response is NODATA (rcode 0, zero answers of the asked type).
- **`aaaaRecords(for query: DNSMessage, ipv6s: [[UInt8]], ttl:) -> Data`** —
  build one response carrying N AAAA answers (one per synthesised address),
  reusing the existing `build(reply:rcode:answers:)`. (The current
  `aaaaRecord` builds a single answer; this generalises to many.)
- **`synthesizeResponse(aaaaQuery: DNSMessage, aResponse: Data, prefix: [UInt8]) -> Data?`** —
  the pure glue: parse the A response, synthesise an AAAA per A record,
  build the AAAA reply matching the original AAAA query's id/question.
  Returns nil if the A response has no usable A records (caller then
  relays the original NODATA).

### 4.2 Config surfacing + decode + channel (PR 2)

- **Controller REST surfacing** (small): the Apple client decodes the REST
  `peerRegisterResponse` (`apps/controller/internal/server/api_peers.go`),
  which currently omits the NAT64 config. The handler already builds its
  response from an in-handler proto `resp` that carries `Dns64Enabled` +
  `Nat64Prefix` (set at `coordinator.go:440-441` via `tenant.DNS64Enabled`
  / `nat64.ResolvePrefix(tenant.NAT64Prefix)`). Add two `omitempty` JSON
  fields to `peerRegisterResponse` and populate them from
  `resp.GetDns64Enabled()` / `resp.GetNat64Prefix()`. No new tenant fetch
  or prefix resolution — purely surfacing existing values, so the gRPC and
  REST register paths stay in lock-step.
- **Register-response decode**: add `dns64Enabled: Bool?` and
  `nat64Prefix: String?` to `BambooClient.RegisterResponse` (the Swift
  REST Decodable, `BambooClient.swift:123`) + their `CodingKeys`, optional
  (nil ⇒ a pre-C4 controller ⇒ treated as off), mirroring the existing
  optional fields (`peerSessionToken`, `preferredRegion`).
- **`MagicDNSPeerStore` platform-conditional path** (§3) — macOS keeps
  `/Users/Shared`; iOS uses the App Group container. Existing macOS
  behaviour is byte-identical; the `init(path:)` test seam is unchanged.
- **NAT64 config sidecar**: a small `NAT64Config { dns64Enabled: Bool;
  nat64Prefix: String }` persisted via the same channel (a sibling file
  `nat64-config.v1.json` in the same directory), written by the app right
  where it already calls `syncMagicDNSMap()`, and read by the DNS proxy
  alongside `store.peers()`. Kept separate from the per-peer map because
  it is tenant-wide, not per-peer.

### 4.3 Provider 2-stage upstream (PR 3)

The macOS and iOS providers are **separate files with duplicated DNS
logic** (`DNSProxy-macOS/DNSProxyProvider.swift`,
`DNSProxy-iOS/DNSProxyProvider.swift`); both are updated.

- **Refactor**: extract today's inline single-shot forwarder into
  `upstreamQuery(_ query: Data, to endpoint: NWHostEndpoint, completion: @escaping (Data?) -> Void)`
  — same NWConnection-per-query + 2 s timeout, but it hands the upstream
  reply bytes to a completion instead of writing straight to the flow. The
  existing `forwardUpstream` becomes `upstreamQuery(..) { data in flow.write(data) }`.
- **DNS64 branch** in `handleOne`: when the query's single question is type
  AAAA (28) for a non-`.bamboo` name **and** `config.dns64Enabled` is true:
  1. `upstreamQuery(originalAAAA)` →
  2. if the reply is **not** NODATA (has ≥1 AAAA answer, or a non-zero
     rcode) → write it to the flow verbatim (real IPv6 wins, per RFC 6147);
  3. if NODATA → build an A query for the same name (new id reuse of the
     original question), `upstreamQuery(aQuery)` →
  4. `synthesizeResponse(originalAAAA, aReply, prefixBytes)`:
     - non-nil → write the synthesised AAAA response to the flow;
     - nil (no A records) → write the original NODATA AAAA reply.
- All other cases (A queries, other types, `.bamboo` names, dns64 off)
  keep the current `MagicDNSResolver.handle` → answered / forwardUpstream
  path unchanged.

## 5. Data flow (the end-to-end picture)

1. An app resolves `ipv4only.example`. The OS sends the DNS proxy both an
   A and an AAAA query.
2. The A query forwards upstream normally and returns the real v4.
3. The AAAA query, with `dns64_enabled` on, forwards upstream; the reply
   is NODATA (the name has no AAAA). The proxy issues an A query, gets the
   v4, and synthesises `<nat64_prefix>::<v4>` as the AAAA answer.
4. The app, preferring IPv6, connects to `<nat64_prefix>::<v4>`. C1's
   AllowedIPs tunnel it to the egress; C2's Tayga translates it to the real
   v4 and MASQUERADEs it out the egress WAN. Replies retrace the path.

## 6. Error handling

- **Upstream timeout/failure** on the AAAA query → behave as today (the
  completion gets nil; write nothing / let the resolver retry) — no
  regression from current forwarding.
- **Upstream failure on the *A* query** (after a NODATA AAAA) → write the
  original NODATA AAAA reply (don't fabricate an answer).
- **Malformed query** → dropped, as today.
- **Non-/96 or empty `nat64_prefix`** while `dns64_enabled` is true → treat
  as dns64 off (skip synthesis); log once. (Defensive — the controller
  always sends a valid prefix when the flag is on.)
- **`.bamboo` names** never synthesise — they are MagicDNS-owned.

## 7. Testing

**Unit-testable (pure, in `AppleSharedTests`, runs in CI):**
- `nat64Synthesize` golden vector `93.184.216.34` → `64:ff9b::5db8:d822`
  plus a custom-/96 vector (matching the Go test's two cases).
- `nat64PrefixBytes` accepts `64:ff9b::/96`, rejects non-/96 and
  IPv4-mapped.
- Answer-section parser on hand-built wire bytes: a single-A response, a
  multi-A response, a NODATA (SOA-only / empty) response.
- `aaaaRecords` / `synthesizeResponse`: feed a parsed AAAA query + a
  built A response, assert the synthesised AAAA answer bytes (id, question
  echoed, N AAAA answers with the embedded v4).
- The DNS64 decision helper: AAAA + external + on → synthesise; AAAA +
  `.bamboo` → no; A query → no; off → no.
- `MagicDNSPeerStore` round-trip via the `init(path:)` seam (unchanged);
  the new `NAT64Config` sidecar round-trip.
- **Controller (real Postgres, in CI):** an e2e REST register test asserting
  the `peerRegisterResponse` JSON carries `dns64Enabled` + `nat64Prefix`
  for a tenant with DNS64 on (mirrors the existing register e2e tests).

**Not unit-testable (NE provider + real host):**
- The 2-stage upstream wiring — compile-only verify
  (`xcodebuild ... CODE_SIGNING_ALLOWED=NO` for macOS where the System
  Extension can't be signed in CI; iOS Simulator build), plus the manual
  hardware E2E below.

**Manual hardware E2E (the done test, user's machines):**
- With a C2 egress live and `dns64_enabled` on, on a macOS client resolve
  a known IPv4-only external name and confirm it returns an AAAA of the
  form `<nat64_prefix>::<v4>`; confirm a TCP/ICMP connection to it reaches
  the destination and `tcpdump` on the egress shows the translated v4
  leaving the WAN (this also re-validates C2 §7). Repeat the resolution
  check on iOS (a connection check too if a reachable egress is available).

## 8. PR breakdown (≈3)

1. **PR 1 — pure synthesis + parsing** (`Shared/` + `AppleSharedTests`):
   `nat64Synthesize`, `nat64PrefixBytes`, the answer-section parser,
   `aaaaRecords`, `synthesizeResponse`, and the DNS64 decision helper.
   Fully CI-tested. No provider/config change.
2. **PR 2 — config surfacing + channel** (controller + Apple): surface
   `dns64Enabled` + `nat64Prefix` on the REST `peerRegisterResponse`
   (controller); decode them in `BambooClient.RegisterResponse` (Swift);
   the `MagicDNSPeerStore` platform-conditional path (reviving iOS
   MagicDNS); the `NAT64Config` sidecar + the app-side write. Controller
   e2e (real Postgres) + store/sidecar unit tests + Apple compile verify.
3. **PR 3 — provider 2-stage upstream**: extract `upstreamQuery`, add the
   DNS64 branch to both the macOS and iOS providers, wiring in the PR 1
   pure functions + the PR 2 config. Compile verify + the §7 manual E2E.

## 9. Phase boundary

C4 completes the NAT64 umbrella's data path on Apple clients: discovery
(DNS64) → routing (C1) → translation (C2). What remains is hardening, not
new data-plane: C3 (egress health probing + multi-egress selection +
status reporting), the RFC 6147 §5.1.4 special-use exclusion, and any
Linux/CLI DNS64 resolver path (currently out — the CLI has no DNS
interception). With C4 merged and the manual E2E green, NAT64 is
end-to-end functional for real external names on macOS and iOS.
