# NAT64 special-use-IPv4 exclusion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the Apple DNS proxy from synthesising AAAA for A records whose IPv4 is a special-use / non-globally-routable address (RFC 6147 §5.1.4), closing the SSRF-into-egress-LAN vector — while keeping `ipv4only.arpa` synthesisable.

**Architecture:** A pure `NAT64Synth.isSpecialUseV4` predicate (mask-loop over a `(network, prefixLen)` table — NOT per-octet, which mis-handles the /10 and /12 masks) + a one-line filter in `DNSMessage.synthesizeResponse` before the synthesise map (empty-after-filter → nil → caller relays the original NODATA). A `.debug` log in both DNS proxy providers distinguishes the special-use skip from a no-A/upstream-fail. Apple-only; no controller/proto/migration change.

**Tech Stack:** Swift / Foundation; XCTest in `AppleSharedTests`; xcodegen project. The pure pieces live in `clients/apple/Shared/{NAT64Synth,MagicDNSResolver}.swift`; the log in `clients/apple/DNSProxy-{macOS,iOS}/DNSProxyProvider.swift`.

---

## Background the implementer needs

- **Spec:** `docs/design/2026-06-06-nat64-special-use-exclusion.md` (§2 architecture + mask-loop mandate, §3 exclusion set + carve-out, §4 observability, §5 tests, §7 known behavior change).
- **Where the synthesis lives** (`clients/apple/Shared/MagicDNSResolver.swift`):
  ```swift
  static func synthesizeResponse(aaaaQuery: DNSMessage, aResponse: Data, prefix: [UInt8]) -> Data? {
      guard let v4s = aRecords(aResponse), !v4s.isEmpty else { return nil }
      let v6s = v4s.map { NAT64Synth.synthesize(prefix: prefix, v4: $0) }
      return aaaaRecords(for: aaaaQuery, ipv6s: v6s)
  }
  ```
  `aRecords(_ Data) -> [[UInt8]]?` returns the 4-byte A rdata values (parse-only). `aaaaRecords(for:ipv6s:)` builds one AAAA answer per address. The filter goes between `aRecords` and the map.
- **`NAT64Synth`** (`clients/apple/Shared/NAT64Synth.swift`) is a `Foundation`-only `enum` (so it compiles into `AppleSharedTests`): `synthesize` (uses `precondition` for its 16/4-byte contract), `prefixBytes`, `shouldSynthesize`. The new predicate joins it.
- **The providers** (`DNSProxy-macOS/DNSProxyProvider.swift` + `DNSProxy-iOS/DNSProxyProvider.swift`, byte-identical per-flow logic) — `maybeDNS64`'s stage-2 branch:
  ```swift
  self.upstreamQuery(aQuery, to: endpoint) { [self] aResp in
      guard let aResp = aResp,
            let synth = DNSMessage.synthesizeResponse(aaaaQuery: msg, aResponse: aResp, prefix: prefix) else {
          self.writeToFlow(aaaaResp, endpoint) // no A records / fail → relay NODATA
          return
      }
      self.writeToFlow(synth, endpoint)
  }
  ```
  `DNSProxyProvider.log` is the `OSLog`; `msg` is the parsed AAAA query (`msg.questions.first?.name` is accessible — `DNSMessage.Question.name` is public). The providers already `os_log(.debug, ...)` elsewhere.
- **Test file:** `clients/apple/Tests/AppleShared/DNS64Tests.swift` (the home for all the unit tests). Helpers already there: `makeQuery(_ name: String, type: UInt16) -> Data`, `makeAResponse(name: String, ips: [[UInt8]]) -> Data`, `DNSMessage.parse`, `DNSMessage.parseAnswers(_) -> [RR]?` (RR has `.type` + `.rdata`). Existing `testSynthesizeResponseSingleA` shows the assertion style (parse `out`, `parseAnswers(out).count`, `rrs[0].rdata == [bytes]`).
- **Synthesised-byte reference:** `64:ff9b::` base = `[0x00,0x64,0xff,0x9b, 0,0,0,0, 0,0,0,0]` then the v4 in bytes 12-15. So `93.184.216.34` → `…,0x5d,0xb8,0xd8,0x22`; `192.0.0.170` → `…,0xc0,0x00,0x00,0xaa`.
- **Build commands:**
  ```bash
  cd clients/apple && xcodegen generate
  xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO 2>&1 | tail -20
  xcodebuild -project bamboo.xcodeproj -scheme BambooApp-macOS -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY="" build 2>&1 | tail -8
  xcodebuild -project bamboo.xcodeproj -scheme BambooApp-iOS -destination 'generic/platform=iOS Simulator' -configuration Debug CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY="" build 2>&1 | tail -8
  ```
  (Standalone SourceKit/LSP diagnostics on these files are KNOWN FALSE POSITIVES for the xcodegen project — trust `xcodebuild`. The iOS app build's only failure may be the pre-existing `libwg-go.a` cgo LINK error, unrelated — judge iOS by the absence of Swift `error:` lines.)

### File structure

| File | Action | Responsibility |
|---|---|---|
| `clients/apple/Shared/NAT64Synth.swift` | Modify | `isSpecialUseV4` + the range table + `matchesPrefix` helper. |
| `clients/apple/Shared/MagicDNSResolver.swift` | Modify | filter in `synthesizeResponse`. |
| `clients/apple/Tests/AppleShared/DNS64Tests.swift` | Modify (append) | boundary tests (Task 1) + filtering tests (Task 2). |
| `clients/apple/DNSProxy-macOS/DNSProxyProvider.swift` | Modify | `.debug` skip log. |
| `clients/apple/DNSProxy-iOS/DNSProxyProvider.swift` | Modify | same log, byte-identical. |
| `docs/deployment/nat64-hardware-e2e.md` | Modify | §7 troubleshooting row. |

---

## Task 1: `NAT64Synth.isSpecialUseV4` (pure mask-table predicate)

**Files:**
- Modify: `clients/apple/Shared/NAT64Synth.swift`
- Test: `clients/apple/Tests/AppleShared/DNS64Tests.swift`

- [ ] **Step 1: Write the failing boundary tests** — append inside `final class DNS64Tests` in `DNS64Tests.swift`:

```swift
    // MARK: isSpecialUseV4

    func testIsSpecialUseV4_excludedRanges() {
        let excluded: [[UInt8]] = [
            [0, 0, 0, 0], [0, 255, 255, 255],            // 0/8
            [10, 0, 0, 0], [10, 255, 255, 255],          // 10/8
            [100, 64, 0, 0], [100, 127, 255, 255],       // 100.64/10 (mesh range)
            [127, 0, 0, 1], [127, 255, 255, 255],        // 127/8 loopback
            [169, 254, 0, 0], [169, 254, 255, 255],      // 169.254/16 link-local
            [172, 16, 0, 0], [172, 31, 255, 255],        // 172.16/12
            [192, 168, 0, 0], [192, 168, 255, 255],      // 192.168/16
            [192, 88, 99, 0], [192, 88, 99, 255],        // 192.88.99/24 6to4
            [198, 18, 0, 0], [198, 19, 255, 255],        // 198.18/15 benchmark
            [224, 0, 0, 0], [239, 255, 255, 255],        // 224/4 multicast
            [240, 0, 0, 0], [255, 255, 255, 255],        // 240/4 reserved + broadcast
            [192, 0, 0, 0], [192, 0, 0, 169], [192, 0, 0, 172], [192, 0, 0, 255], // 192.0.0/24 except .170/.171
        ]
        for v4 in excluded {
            XCTAssertTrue(NAT64Synth.isSpecialUseV4(v4), "\(v4) should be special-use")
        }
    }

    func testIsSpecialUseV4_synthesizable() {
        let ok: [[UInt8]] = [
            [93, 184, 216, 34], [8, 8, 8, 8], [1, 1, 1, 1],   // global unicast
            [203, 0, 113, 5],                                  // TEST-NET-3 (kept — runbook example)
            [192, 0, 0, 170], [192, 0, 0, 171],                // ipv4only.arpa carve-out
            // non-aligned-mask NEAR-boundaries that must NOT match:
            [100, 63, 255, 255], [100, 128, 0, 0],             // just outside 100.64/10
            [172, 15, 255, 255], [172, 32, 0, 0],              // just outside 172.16/12
            [198, 17, 255, 255], [198, 20, 0, 0],              // just outside 198.18/15
            [169, 253, 255, 255], [169, 255, 0, 0],            // just outside 169.254/16
            [192, 88, 98, 255], [192, 88, 100, 0],             // just outside 192.88.99/24
            [223, 255, 255, 255],                              // just below 224/4 multicast
        ]
        for v4 in ok {
            XCTAssertFalse(NAT64Synth.isSpecialUseV4(v4), "\(v4) should be synthesizable")
        }
    }

    func testIsSpecialUseV4_badLength() {
        XCTAssertFalse(NAT64Synth.isSpecialUseV4([10, 0, 0]))       // 3 bytes → false (guard)
        XCTAssertFalse(NAT64Synth.isSpecialUseV4([10, 0, 0, 0, 0])) // 5 bytes → false
    }
```
(`223.255.255.255` is the last address before `224.0.0.0` — a synthesizable boundary; the `224/4` multicast block starts at `224.0.0.0`, asserted special-use in `testIsSpecialUseV4_excludedRanges`.)

- [ ] **Step 2: Run to verify it fails**

```bash
cd clients/apple && xcodegen generate
xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests \
  -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO \
  -only-testing:AppleSharedTests/DNS64Tests 2>&1 | tail -15
```
Expected: FAIL — `type 'NAT64Synth' has no member 'isSpecialUseV4'`.

- [ ] **Step 3: Implement `isSpecialUseV4`** — add to `enum NAT64Synth` in `NAT64Synth.swift`:

```swift
    /// isSpecialUseV4 reports whether a 4-byte IPv4 must NOT be synthesised
    /// into a NAT64 AAAA — a special-use / non-globally-routable address per
    /// RFC 6052 §3.1 ("MUST NOT translate non-global IPv4"), RFC 6147 §5.1.4
    /// (the DNS64 exclusion set), and the IANA IPv4 Special-Purpose Registry
    /// (RFC 6890, Global=False). Synthesising one would let the egress's
    /// Tayga translate it onto the egress's own LAN (SSRF) — the 100.64/10
    /// CGN entry in particular covers the bamboo mesh's own tunnel range.
    ///
    /// Carve-out: 192.0.0.170/.171 (ipv4only.arpa, RFC 7335) stay
    /// SYNTHESISABLE even though they sit inside the 192.0.0.0/24 exclusion —
    /// the hardware-E2E runbook uses ipv4only.arpa as its synthesis
    /// smoke-test (see docs/deployment/nat64-hardware-e2e.md §2). Do not
    /// "tidy away" that carve-out.
    ///
    /// Non-4-byte input returns false (callers pass aRecords' 4-byte rdata).
    static func isSpecialUseV4(_ v4: [UInt8]) -> Bool {
        guard v4.count == 4 else { return false }
        // Carve-out checked first so it short-circuits the /24 below.
        if v4 == [192, 0, 0, 170] || v4 == [192, 0, 0, 171] {
            return false
        }
        for range in specialUseV4Ranges where v4MatchesPrefix(v4, range.net, range.bits) {
            return true
        }
        return false
    }

    /// specialUseV4Ranges is the (network, prefix-length) exclusion table.
    /// Provenance above; keep alphabetical-by-first-octet for auditability.
    private static let specialUseV4Ranges: [(net: [UInt8], bits: Int)] = [
        ([0, 0, 0, 0], 8),        // this host on this network
        ([10, 0, 0, 0], 8),       // RFC 1918 private
        ([100, 64, 0, 0], 10),    // CGN (RFC 6598) — covers the mesh 100.127.0.0/24
        ([127, 0, 0, 0], 8),      // loopback
        ([169, 254, 0, 0], 16),   // link-local
        ([172, 16, 0, 0], 12),    // RFC 1918 private
        ([192, 0, 0, 0], 24),     // IETF protocol assignments (except .170/.171, carved out)
        ([192, 88, 99, 0], 24),   // 6to4 relay anycast (RFC 3068, deprecated RFC 7526)
        ([192, 168, 0, 0], 16),   // RFC 1918 private
        ([198, 18, 0, 0], 15),    // benchmarking (RFC 2544)
        ([224, 0, 0, 0], 4),      // multicast (never a unicast A destination)
        ([240, 0, 0, 0], 4),      // reserved (incl. 255.255.255.255 broadcast)
    ]

    /// v4MatchesPrefix compares the first `bits` bits of v4 against net:
    /// full bytes via ==, the final partial byte via a high-bit mask. One
    /// path for octet-aligned and non-aligned (/10, /12, /15) masks — a
    /// per-octet first-byte check would mis-handle CGN/RFC1918 and re-open
    /// the SSRF vector.
    private static func v4MatchesPrefix(_ v4: [UInt8], _ net: [UInt8], _ bits: Int) -> Bool {
        var remaining = bits
        for i in 0..<4 {
            if remaining >= 8 {
                if v4[i] != net[i] { return false }
                remaining -= 8
            } else if remaining > 0 {
                let mask = UInt8(0xFF << (8 - remaining))
                if (v4[i] & mask) != (net[i] & mask) { return false }
                remaining = 0
            } else {
                break
            }
        }
        return true
    }
```

- [ ] **Step 4: Run to verify it passes** (after fixing the deliberately-wrong test line per Step 1's note)

```bash
cd clients/apple && xcodegen generate
xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests \
  -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO \
  -only-testing:AppleSharedTests/DNS64Tests 2>&1 | tail -15
```
Expected: `** TEST SUCCEEDED **` — the 3 new tests + all prior DNS64Tests pass.

- [ ] **Step 5: Commit**

```bash
git add clients/apple/Shared/NAT64Synth.swift clients/apple/Tests/AppleShared/DNS64Tests.swift
git commit -m "feat(apple): NAT64Synth.isSpecialUseV4 — RFC 6147 §5.1.4 exclusion table (NAT64 hardening)"
```

---

## Task 2: filter in `synthesizeResponse`

**Files:**
- Modify: `clients/apple/Shared/MagicDNSResolver.swift`
- Test: `clients/apple/Tests/AppleShared/DNS64Tests.swift`

- [ ] **Step 1: Write the failing filtering tests** — append inside `final class DNS64Tests`:

```swift
    // MARK: synthesizeResponse special-use filtering

    func testSynthesizeResponse_dropsPrivateKeepsPublic() {
        let aaaaQ = DNSMessage.parse(makeQuery("mixed.example", type: 28))!
        // one public + one RFC1918 private A → only the public is synthesised.
        let aResp = makeAResponse(name: "mixed.example", ips: [[93, 184, 216, 34], [192, 168, 1, 1]])
        let prefix = NAT64Synth.prefixBytes("64:ff9b::/96")!
        let out = DNSMessage.synthesizeResponse(aaaaQuery: aaaaQ, aResponse: aResp, prefix: prefix)!

        let rrs = DNSMessage.parseAnswers(out)!
        XCTAssertEqual(rrs.count, 1, "only the public A should be synthesised")
        XCTAssertEqual(rrs[0].rdata,
                       [0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0, 0x5d, 0xb8, 0xd8, 0x22])
    }

    func testSynthesizeResponse_allPrivateReturnsNil() {
        let aaaaQ = DNSMessage.parse(makeQuery("priv.example", type: 28))!
        let aResp = makeAResponse(name: "priv.example", ips: [[10, 0, 0, 1], [192, 168, 0, 1]])
        let prefix = NAT64Synth.prefixBytes("64:ff9b::/96")!
        // all special-use → nil → caller relays the original NODATA AAAA.
        XCTAssertNil(DNSMessage.synthesizeResponse(aaaaQuery: aaaaQ, aResponse: aResp, prefix: prefix))
    }

    func testSynthesizeResponse_ipv4onlyArpaStillSynthesises() {
        let aaaaQ = DNSMessage.parse(makeQuery("ipv4only.arpa", type: 28))!
        let aResp = makeAResponse(name: "ipv4only.arpa", ips: [[192, 0, 0, 170]])
        let prefix = NAT64Synth.prefixBytes("64:ff9b::/96")!
        let out = DNSMessage.synthesizeResponse(aaaaQuery: aaaaQ, aResponse: aResp, prefix: prefix)!

        let rrs = DNSMessage.parseAnswers(out)!
        XCTAssertEqual(rrs.count, 1)
        XCTAssertEqual(rrs[0].rdata,
                       [0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0, 0xc0, 0x00, 0x00, 0xaa])
    }
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd clients/apple && xcodegen generate
xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests \
  -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO \
  -only-testing:AppleSharedTests/DNS64Tests 2>&1 | tail -15
```
Expected: FAIL — `testSynthesizeResponse_dropsPrivateKeepsPublic` gets 2 answers (both synthesised) not 1; `testSynthesizeResponse_allPrivateReturnsNil` is non-nil. (`ipv4onlyArpa` may already pass since the filter isn't in yet.)

- [ ] **Step 3: Add the filter to `synthesizeResponse`** in `MagicDNSResolver.swift`. Replace the function body:

```swift
    static func synthesizeResponse(aaaaQuery: DNSMessage,
                                   aResponse: Data,
                                   prefix: [UInt8]) -> Data? {
        guard let v4s = aRecords(aResponse) else { return nil }
        // Drop special-use / non-globally-routable v4s (RFC 6147 §5.1.4) so
        // we never synthesise a route the egress would translate onto its own
        // LAN. A mixed response keeps only the synthesisable (global) answers;
        // an all-special-use response yields nil → caller relays NODATA.
        let synthesizable = v4s.filter { !NAT64Synth.isSpecialUseV4($0) }
        guard !synthesizable.isEmpty else { return nil }
        let v6s = synthesizable.map { NAT64Synth.synthesize(prefix: prefix, v4: $0) }
        return aaaaRecords(for: aaaaQuery, ipv6s: v6s)
    }
```
(The old `guard let v4s = aRecords(aResponse), !v4s.isEmpty else { return nil }` is replaced by the two guards above — the no-A-records case still returns nil via `synthesizable.isEmpty`.)

- [ ] **Step 4: Run to verify it passes**

```bash
cd clients/apple && xcodegen generate
xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests \
  -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO \
  -only-testing:AppleSharedTests/DNS64Tests 2>&1 | tail -15
```
Expected: `** TEST SUCCEEDED **` — the 3 new filtering tests + the pre-existing `testSynthesizeResponse*` (single/multi/nil-when-no-A) still pass (a public-only response is unaffected by the filter).

- [ ] **Step 5: Commit**

```bash
git add clients/apple/Shared/MagicDNSResolver.swift clients/apple/Tests/AppleShared/DNS64Tests.swift
git commit -m "feat(apple): filter special-use v4s in synthesizeResponse (NAT64 hardening)"
```

---

## Task 3: provider `.debug` skip log (macOS + iOS)

**Files:**
- Modify: `clients/apple/DNSProxy-macOS/DNSProxyProvider.swift`
- Modify: `clients/apple/DNSProxy-iOS/DNSProxyProvider.swift`

No unit test (NE provider) — compile-verified + the byte-identical diff gate. The skip is now possible (Task 2 can return nil for an all-special-use response); this makes it diagnosable.

- [ ] **Step 1: Add the log in `maybeDNS64`'s stage-2 nil branch (macOS).** In `DNSProxy-macOS/DNSProxyProvider.swift`, find the stage-2 `upstreamQuery(aQuery, …)` closure's `guard … synthesizeResponse … else { … }`. Replace that else block:

```swift
                guard let aResp = aResp,
                      let synth = DNSMessage.synthesizeResponse(
                          aaaaQuery: msg, aResponse: aResp, prefix: prefix) else {
                    self.writeToFlow(aaaaResp, endpoint) // no A records / fail → relay NODATA
                    return
                }
```
with:
```swift
                guard let aResp = aResp,
                      let synth = DNSMessage.synthesizeResponse(
                          aaaaQuery: msg, aResponse: aResp, prefix: prefix) else {
                    // Distinguish "all A special-use" (NAT64 §5.1.4 exclusion)
                    // from "no A / upstream fail" so an operator can diagnose a
                    // name that resolves but never gets a synthesised AAAA.
                    if let aResp = aResp, let v4s = DNSMessage.aRecords(aResp), !v4s.isEmpty {
                        os_log(.debug, log: DNSProxyProvider.log,
                               "dns64 skip: all %{public}d A special-use for %{public}@",
                               v4s.count, msg.questions.first?.name ?? "?")
                    }
                    self.writeToFlow(aaaaResp, endpoint) // no A / fail / all special-use → relay NODATA
                    return
                }
```

- [ ] **Step 2: Apply the IDENTICAL change to the iOS provider.** In `DNSProxy-iOS/DNSProxyProvider.swift`, make the same edit to the same stage-2 else block.

- [ ] **Step 3: Verify the two providers' DNS64 code stays byte-identical** (the diff gate from C4 PR3):

```bash
cd "$(git rev-parse --show-toplevel)"
diff \
  <(sed -n '/private func writeToFlow/,/^}/p' clients/apple/DNSProxy-macOS/DNSProxyProvider.swift) \
  <(sed -n '/private func writeToFlow/,/^}/p' clients/apple/DNSProxy-iOS/DNSProxyProvider.swift) \
  && echo "IDENTICAL"
```
Expected: `IDENTICAL`. (If they differ, reconcile iOS to match macOS exactly.)

- [ ] **Step 4: Compile-verify both providers**

```bash
cd clients/apple && xcodegen generate
echo "=== macOS ===" && xcodebuild -project bamboo.xcodeproj -scheme BambooApp-macOS \
  -destination 'platform=macOS' -configuration Debug \
  CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY="" build 2>&1 | tail -6
echo "=== iOS ===" && xcodebuild -project bamboo.xcodeproj -scheme BambooApp-iOS \
  -destination 'generic/platform=iOS Simulator' -configuration Debug \
  CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY="" build 2>&1 | tee /tmp/su-ios.log | tail -6
grep -E "error: " /tmp/su-ios.log | grep -iE "clients/apple|DNSProxy" || echo "NO SWIFT COMPILE ERRORS"
```
Expected: macOS `** BUILD SUCCEEDED **`; iOS `NO SWIFT COMPILE ERRORS` (only the pre-existing `libwg-go.a` link error, unrelated).

- [ ] **Step 5: Commit**

```bash
git add clients/apple/DNSProxy-macOS/DNSProxyProvider.swift clients/apple/DNSProxy-iOS/DNSProxyProvider.swift
git commit -m "feat(apple): debug-log DNS64 special-use skip in both providers (NAT64 hardening)"
```

---

## Task 4: runbook troubleshooting row + full verification

**Files:**
- Modify: `docs/deployment/nat64-hardware-e2e.md`

- [ ] **Step 1: Add a troubleshooting row.** In `docs/deployment/nat64-hardware-e2e.md` §7's troubleshooting table, add a row (after the existing rows):

```markdown
| IPv4-only 名稱解析但回 NODATA(沒合成 AAAA) | 該名稱的上游 A 是否為**特殊用途/私有位址**(10.x / 172.16-31.x / 192.168.x / 100.64-127.x / 127.x / 169.254.x / 0.x / 224-255.x)?**設計上刻意不對這些合成**(RFC 6147 §5.1.4 + egress-LAN SSRF 防護),所以私有 split-horizon 目的地預設不經 NAT64 可達。DNS proxy 會在 `.debug` log `dns64 skip: all N A special-use for <name>`。`ipv4only.arpa`(192.0.0.170/.171)是刻意保留的例外仍會合成。 |
```

- [ ] **Step 2: Full AppleSharedTests bundle + both builds + diff gate**

```bash
cd clients/apple && xcodegen generate
echo "=== AppleSharedTests ===" && xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests \
  -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO 2>&1 | tail -15
echo "=== macOS build ===" && xcodebuild -project bamboo.xcodeproj -scheme BambooApp-macOS \
  -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY="" build 2>&1 | tail -4
echo "=== iOS Swift compile ===" && xcodebuild -project bamboo.xcodeproj -scheme BambooApp-iOS \
  -destination 'generic/platform=iOS Simulator' -configuration Debug CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY="" build 2>&1 | tee /tmp/su-ios2.log | tail -4
grep -E "error: " /tmp/su-ios2.log | grep -iE "clients/apple|DNSProxy" || echo "NO SWIFT COMPILE ERRORS"
echo "=== provider diff gate ===" && cd "$(git rev-parse --show-toplevel)" && diff \
  <(sed -n '/private func writeToFlow/,/^}/p' clients/apple/DNSProxy-macOS/DNSProxyProvider.swift) \
  <(sed -n '/private func writeToFlow/,/^}/p' clients/apple/DNSProxy-iOS/DNSProxyProvider.swift) && echo "IDENTICAL"
```
Expected: `** TEST SUCCEEDED **` (whole bundle incl. the new isSpecialUseV4 + filtering tests); macOS `** BUILD SUCCEEDED **`; iOS `NO SWIFT COMPILE ERRORS`; providers `IDENTICAL`.

- [ ] **Step 3: Confirm clean tree + commit list**

```bash
cd "$(git rev-parse --show-toplevel)" && git status --short && git log --oneline origin/main..HEAD
```
Expected: clean tree (`bamboo.xcodeproj` gitignored); branch shows the spec + plan + 3 feat commits + this docs commit.

- [ ] **Step 4: Commit**

```bash
git add docs/deployment/nat64-hardware-e2e.md
git commit -m "docs(nat64): runbook troubleshooting row for special-use exclusion (NAT64 hardening)"
```

---

## Self-review notes (for the implementer)

- **Spec coverage:** §2 architecture → Tasks 1+2 (predicate + filter, mask-loop mandated in Task 1's `v4MatchesPrefix`); §3 exclusion set + carve-out → Task 1's table + the `192.0.0.170/.171` short-circuit + the boundary tests; §4 observability → Task 3; §5 tests → Task 1 (boundaries incl. non-aligned masks) + Task 2 (mixed/all-private/carve-out); §7 known behavior change → Task 4's runbook row. §6 (egress-side follow-up + per-tenant allowlist) is explicitly OUT of this plan.
- **Type consistency:** `NAT64Synth.isSpecialUseV4(_ [UInt8]) -> Bool`; `specialUseV4Ranges: [(net: [UInt8], bits: Int)]`; `v4MatchesPrefix(_ [UInt8], _ [UInt8], _ Int) -> Bool`; `synthesizeResponse(aaaaQuery:aResponse:prefix:) -> Data?` (unchanged signature, body filters). All aligned.
- **The mask-loop is the load-bearing correctness item** — Task 1's boundary tests for the /10, /12, /15 masks (100.63/100.64/100.127/100.128, 172.15/172.16/172.31/172.32, 198.17/198.18/198.19/198.20) are the regression guard against a naive first-octet rewrite; do not weaken them.
- **The carve-out is checked BEFORE the table** so `192.0.0.170/.171` short-circuit to false even though they're inside `192.0.0.0/24`.
- **YAGNI / scope:** Apple-only; no controller/proto/migration; no egress-side filter (spec §6 follow-up); TEST-NET kept synthesisable (runbook example).
