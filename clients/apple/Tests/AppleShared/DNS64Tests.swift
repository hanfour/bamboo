// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for the pure DNS64 layer (NAT64 Phase C4 PR 1): RFC 6052
/// synthesis, NAT64 prefix parsing, the DNS answer-section parser, the
/// multi-AAAA builder + synthesis glue, and the query-eligibility
/// decision. No NetworkExtension deps. The synthesis vector mirrors the
/// controller's Go nat64.Synthesize test so the two stay byte-identical.
final class DNS64Tests: XCTestCase {

    // MARK: NAT64Synth.synthesize / prefixBytes

    func testSynthesizeKnownVector() {
        // 64:ff9b::/96 + 93.184.216.34 -> 64:ff9b::5db8:d822
        let prefix = NAT64Synth.prefixBytes("64:ff9b::/96")!
        let got = NAT64Synth.synthesize(prefix: prefix, v4: [93, 184, 216, 34])
        let want: [UInt8] = [0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0, 0x5d, 0xb8, 0xd8, 0x22]
        XCTAssertEqual(got, want)
    }

    func testSynthesizeCustomPrefix() {
        // 2001:db8:1234::/96 + 93.184.216.34 -> 2001:db8:1234::5db8:d822
        let prefix = NAT64Synth.prefixBytes("2001:db8:1234::/96")!
        let got = NAT64Synth.synthesize(prefix: prefix, v4: [93, 184, 216, 34])
        let want: [UInt8] = [0x20, 0x01, 0x0d, 0xb8, 0x12, 0x34, 0, 0, 0, 0, 0, 0, 0x5d, 0xb8, 0xd8, 0x22]
        XCTAssertEqual(got, want)
    }

    func testPrefixBytesRejectsBadInput() {
        XCTAssertNil(NAT64Synth.prefixBytes("64:ff9b::/64"))   // not /96
        XCTAssertNil(NAT64Synth.prefixBytes("64:ff9b::"))      // no suffix
        XCTAssertNil(NAT64Synth.prefixBytes("nonsense/96"))    // unparseable addr
    }

    func testPrefixBytesZeroesLow32() {
        let got = NAT64Synth.prefixBytes("64:ff9b::5db8:d822/96")!
        let want: [UInt8] = [0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
        XCTAssertEqual(got, want)
    }

    // MARK: wire-format test helpers (used by later tasks too)

    /// makeQuery encodes a DNS query for `name` (uncompressed) with `type`,
    /// id 0x1234, RD set, IN class.
    func makeQuery(_ name: String, type: UInt16) -> Data {
        var d = Data()
        func u16(_ v: UInt16) { d.append(UInt8(v >> 8)); d.append(UInt8(v & 0xff)) }
        u16(0x1234)                         // id
        u16(0x0100)                         // flags: RD
        u16(1); u16(0); u16(0); u16(0)      // qd=1, an=ns=ar=0
        for label in name.split(separator: ".") {
            d.append(UInt8(label.count))
            d.append(contentsOf: Array(label.utf8))
        }
        d.append(0)                         // root label
        u16(type)                           // qtype
        u16(1)                              // qclass IN
        return d
    }

    /// makeAResponse encodes a NOERROR response to an A query for `name`
    /// carrying one A record per `ips` entry. Answer names use a
    /// compression pointer to the question name (0xC00C), exercising the
    /// parser's pointer path the way real resolvers do.
    func makeAResponse(name: String, ips: [[UInt8]]) -> Data {
        var d = Data()
        func u16(_ v: UInt16) { d.append(UInt8(v >> 8)); d.append(UInt8(v & 0xff)) }
        func u32(_ v: UInt32) {
            d.append(UInt8((v >> 24) & 0xff)); d.append(UInt8((v >> 16) & 0xff))
            d.append(UInt8((v >> 8) & 0xff));  d.append(UInt8(v & 0xff))
        }
        u16(0x1234)                              // id
        u16(0x8180)                              // QR + RD + RA, rcode 0
        u16(1); u16(UInt16(ips.count)); u16(0); u16(0)
        for label in name.split(separator: ".") {
            d.append(UInt8(label.count))
            d.append(contentsOf: Array(label.utf8))
        }
        d.append(0); u16(1); u16(1)              // question: A, IN
        for ip in ips {
            d.append(0xC0); d.append(0x0C)       // name = pointer to offset 12
            u16(1); u16(1); u32(60); u16(4)      // A, IN, ttl 60, rdlength 4
            d.append(contentsOf: ip)
        }
        return d
    }
}
