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

    func testPrefixBytesRejectsIPv4Mapped() {
        // ::ffff:0:0/96 is the IPv4-mapped range — not a valid NAT64 prefix.
        XCTAssertNil(NAT64Synth.prefixBytes("::ffff:0:0/96"))
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

    // MARK: DNSMessage answer parsing

    func testParseSingleARecord() {
        let resp = makeAResponse(name: "ipv4only.example", ips: [[93, 184, 216, 34]])
        XCTAssertEqual(DNSMessage.aRecords(resp), [[93, 184, 216, 34]])
    }

    func testParseMultipleARecords() {
        let resp = makeAResponse(name: "multi.example", ips: [[1, 1, 1, 1], [8, 8, 8, 8]])
        XCTAssertEqual(DNSMessage.aRecords(resp), [[1, 1, 1, 1], [8, 8, 8, 8]])
    }

    func testIsDNS64CandidateTrueForNoData() {
        // A NOERROR/empty AAAA response (the IPv4-only case) is a candidate.
        let aaaaQ = DNSMessage.parse(makeQuery("ipv4only.example", type: 28))!
        let nodata = DNSMessage.noerrorEmpty(for: aaaaQ)
        XCTAssertTrue(DNSMessage.isDNS64Candidate(nodata))
    }

    func testIsDNS64CandidateFalseWhenRealAAAA() {
        let aaaaQ = DNSMessage.parse(makeQuery("dual.example", type: 28))!
        let v6: [UInt8] = [0x26, 0x06, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1]
        let withAAAA = DNSMessage.aaaaRecord(for: aaaaQ, ipv6: v6)
        XCTAssertFalse(DNSMessage.isDNS64Candidate(withAAAA))
    }

    func testParseAnswersRejectsTruncated() {
        XCTAssertNil(DNSMessage.aRecords(Data([0x12, 0x34])))            // < 12-byte header
        // Header claims 1 answer but the body is missing.
        let bogus = Data([0x12, 0x34, 0x81, 0x80, 0, 0, 0, 1, 0, 0, 0, 0])
        XCTAssertNil(DNSMessage.aRecords(bogus))
    }

    // MARK: synthesis glue

    func testSynthesizeResponseSingleA() {
        let aaaaQ = DNSMessage.parse(makeQuery("ipv4only.example", type: 28))!
        let aResp = makeAResponse(name: "ipv4only.example", ips: [[93, 184, 216, 34]])
        let prefix = NAT64Synth.prefixBytes("64:ff9b::/96")!
        let out = DNSMessage.synthesizeResponse(aaaaQuery: aaaaQ, aResponse: aResp, prefix: prefix)!

        let parsed = DNSMessage.parse(out)!
        XCTAssertEqual(parsed.id, 0x1234)                 // echoes the AAAA query id
        let rrs = DNSMessage.parseAnswers(out)!
        XCTAssertEqual(rrs.count, 1)
        XCTAssertEqual(rrs[0].type, 28)
        XCTAssertEqual(rrs[0].rdata,
                       [0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0, 0x5d, 0xb8, 0xd8, 0x22])
    }

    func testSynthesizeResponseMultiA() {
        let aaaaQ = DNSMessage.parse(makeQuery("multi.example", type: 28))!
        let aResp = makeAResponse(name: "multi.example", ips: [[1, 1, 1, 1], [8, 8, 8, 8]])
        let prefix = NAT64Synth.prefixBytes("64:ff9b::/96")!
        let out = DNSMessage.synthesizeResponse(aaaaQuery: aaaaQ, aResponse: aResp, prefix: prefix)!

        let rrs = DNSMessage.parseAnswers(out)!
        XCTAssertEqual(rrs.count, 2)
        XCTAssertEqual(Array(rrs[0].rdata.suffix(4)), [1, 1, 1, 1])
        XCTAssertEqual(Array(rrs[1].rdata.suffix(4)), [8, 8, 8, 8])
    }

    func testSynthesizeResponseNilWhenNoARecords() {
        let aaaaQ = DNSMessage.parse(makeQuery("empty.example", type: 28))!
        let aQ = DNSMessage.parse(makeQuery("empty.example", type: 1))!
        let emptyA = DNSMessage.noerrorEmpty(for: aQ)         // A response with no answers
        let prefix = NAT64Synth.prefixBytes("64:ff9b::/96")!
        XCTAssertNil(DNSMessage.synthesizeResponse(aaaaQuery: aaaaQ, aResponse: emptyA, prefix: prefix))
    }

    // MARK: query-eligibility decision

    func testShouldSynthesizeExternalAAAA() {
        let q = DNSMessage.parse(makeQuery("ipv4only.example", type: 28))!
        XCTAssertTrue(NAT64Synth.shouldSynthesize(query: q, dns64Enabled: true, zone: "bamboo"))
    }

    func testShouldSynthesizeFalseWhenDisabled() {
        let q = DNSMessage.parse(makeQuery("ipv4only.example", type: 28))!
        XCTAssertFalse(NAT64Synth.shouldSynthesize(query: q, dns64Enabled: false, zone: "bamboo"))
    }

    func testShouldSynthesizeSkipsBambooZone() {
        let q = DNSMessage.parse(makeQuery("host.bamboo", type: 28))!
        XCTAssertFalse(NAT64Synth.shouldSynthesize(query: q, dns64Enabled: true, zone: "bamboo"))
    }

    func testShouldSynthesizeSkipsBambooApex() {
        let q = DNSMessage.parse(makeQuery("bamboo", type: 28))!
        XCTAssertFalse(NAT64Synth.shouldSynthesize(query: q, dns64Enabled: true, zone: "bamboo"))
    }

    func testShouldSynthesizeSkipsAQuery() {
        let q = DNSMessage.parse(makeQuery("ipv4only.example", type: 1))!
        XCTAssertFalse(NAT64Synth.shouldSynthesize(query: q, dns64Enabled: true, zone: "bamboo"))
    }

    // MARK: aQueryFromAAAA

    func testAQueryFromAAAAFlipsType() {
        let aaaa = makeQuery("ipv4only.example", type: 28)
        let a = DNSMessage.aQueryFromAAAA(aaaa)!
        let parsed = DNSMessage.parse(a)!
        XCTAssertEqual(parsed.id, 0x1234)                       // id preserved
        XCTAssertEqual(parsed.questions.count, 1)
        XCTAssertEqual(parsed.questions[0].name, "ipv4only.example.")
        XCTAssertEqual(parsed.questions[0].qtype, .a)           // 28 → 1
        XCTAssertEqual(parsed.questions[0].qclass, 1)           // IN preserved
        XCTAssertEqual(a.count, aaaa.count)                     // only 2 bytes changed
    }

    func testAQueryFromAAAARejectsNonAAAA() {
        // An A query (or anything not a single AAAA question) → nil.
        XCTAssertNil(DNSMessage.aQueryFromAAAA(makeQuery("x.example", type: 1)))
    }

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
}
