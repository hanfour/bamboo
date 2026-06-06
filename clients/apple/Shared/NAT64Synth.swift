// SPDX-License-Identifier: Apache-2.0

import Foundation

/// NAT64Synth holds the pure RFC 6052 / RFC 6147 helpers the DNS proxy
/// uses for DNS64: embedding an IPv4 address into a NAT64 /96 prefix,
/// parsing the controller-supplied prefix string, and deciding whether a
/// query is eligible for synthesis. No NetworkExtension or
/// Foundation-networking dependencies, so it compiles into AppleSharedTests.
enum NAT64Synth {
    /// synthesize embeds a 4-byte IPv4 into the low 32 bits of a 16-byte
    /// /96 NAT64 prefix (RFC 6052 §2.2). Matches the controller's Go
    /// nat64.Synthesize byte-for-byte. `prefix` must be 16 bytes (its last
    /// 4 are overwritten); `v4` must be 4 bytes.
    static func synthesize(prefix: [UInt8], v4: [UInt8]) -> [UInt8] {
        precondition(prefix.count == 16, "prefix must be 16 bytes")
        precondition(v4.count == 4, "v4 must be 4 bytes")
        var out = prefix
        out[12] = v4[0]
        out[13] = v4[1]
        out[14] = v4[2]
        out[15] = v4[3]
        return out
    }

    /// prefixBytes parses a controller-supplied NAT64 prefix string of the
    /// form "<ipv6>/96" into its 16-byte base (low 32 bits zeroed). The
    /// controller already validates the prefix, so this is lenient: it
    /// requires a "/96" suffix and a parseable IPv6 address, returning nil
    /// otherwise (the caller then treats DNS64 as off).
    static func prefixBytes(_ s: String) -> [UInt8]? {
        let parts = s.split(separator: "/", maxSplits: 1, omittingEmptySubsequences: false)
        guard parts.count == 2, parts[1] == "96" else { return nil }
        guard var bytes = MagicDNSResolver.ipv6Bytes(String(parts[0])), bytes.count == 16 else {
            return nil
        }
        // Reject an IPv4-mapped prefix (::ffff:0:0/96): bytes 0-9 zero and
        // bytes 10-11 == 0xff. The controller's ParsePrefix rejects these;
        // mirror it so a degenerate prefix never reaches synthesis.
        if bytes[0..<10].allSatisfy({ $0 == 0 }) && bytes[10] == 0xff && bytes[11] == 0xff {
            return nil
        }
        bytes[12] = 0
        bytes[13] = 0
        bytes[14] = 0
        bytes[15] = 0
        return bytes
    }

    /// shouldSynthesize decides whether a DNS *query* is eligible for
    /// DNS64 AAAA synthesis: DNS64 must be enabled, and the query must be a
    /// single AAAA (type 28) question for a name OUTSIDE the MagicDNS zone
    /// (".<zone>." and the apex "<zone>." are answered locally, never
    /// synthesized). The actual NODATA check happens on the upstream
    /// response (DNSMessage.isDNS64Candidate); this gates the query.
    static func shouldSynthesize(query: DNSMessage, dns64Enabled: Bool, zone: String) -> Bool {
        guard dns64Enabled, query.questions.count == 1 else { return false }
        let q = query.questions[0]
        guard q.qtype == .aaaa else { return false }
        let name = q.name.lowercased()
        let z = zone.lowercased()
        if name == z + "." || name.hasSuffix("." + z + ".") { return false }
        return true
    }

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
                // truncatingIfNeeded keeps only the low 8 bits of the
                // Int-width shift result (e.g. 0xFF<<4 = 4080 → 0xF0 = 240).
                let mask = UInt8(truncatingIfNeeded: 0xFF << (8 - remaining))
                if (v4[i] & mask) != (net[i] & mask) { return false }
                remaining = 0
            } else {
                break
            }
        }
        return true
    }
}
