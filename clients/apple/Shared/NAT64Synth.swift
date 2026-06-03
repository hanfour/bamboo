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
        bytes[12] = 0
        bytes[13] = 0
        bytes[14] = 0
        bytes[15] = 0
        return bytes
    }
}
