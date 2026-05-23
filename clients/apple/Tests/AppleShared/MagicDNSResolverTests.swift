// SPDX-License-Identifier: Apache-2.0

import XCTest

/// Tests for the MagicDNS resolver — pure DNS-message parser and
/// response builder, no NetworkExtension dependencies. Tests build
/// queries by hand (DNS wire format) and assert on the parsed
/// outcome + the synthesized response bytes for the answered cases.
final class MagicDNSResolverTests: XCTestCase {

    // MARK: - fixtures

    private let peers: [String: MagicDNSPeerStore.PeerEntry] = [
        "mac-mini": .init(ipv4: "100.64.0.1",
                          ipv6: nil,
                          hostname: "Mac mini"),
        "h4": .init(ipv4: "100.64.0.3",
                    ipv6: "fd7a:115c:a1e0::3",
                    hostname: "H4"),
    ]

    /// Build a minimal A-record DNS query for the given name.
    /// Header: id=0x4242, flags=0x0100 (standard query, recursion
    /// desired), qdcount=1. Question: name in label-prefixed form,
    /// QType=1 (A), QClass=1 (IN).
    private func aQuery(for name: String, id: UInt16 = 0x4242) -> Data {
        return query(name: name, qtype: 1, id: id)
    }

    private func aaaaQuery(for name: String, id: UInt16 = 0x4242) -> Data {
        return query(name: name, qtype: 28, id: id)
    }

    private func query(name: String, qtype: UInt16, id: UInt16) -> Data {
        var data = Data()
        // Header
        data.append(UInt8(id >> 8))
        data.append(UInt8(id & 0xFF))
        data.append(contentsOf: [0x01, 0x00])  // flags: RD=1
        data.append(contentsOf: [0x00, 0x01])  // qdcount=1
        data.append(contentsOf: [0x00, 0x00, 0x00, 0x00, 0x00, 0x00])  // an/ns/ar=0
        // Question — labels
        for label in name.split(separator: ".") {
            let bytes = Array(label.utf8)
            data.append(UInt8(bytes.count))
            data.append(contentsOf: bytes)
        }
        data.append(0)  // root terminator
        data.append(UInt8(qtype >> 8))
        data.append(UInt8(qtype & 0xFF))
        data.append(contentsOf: [0x00, 0x01])  // QClass IN
        return data
    }

    // MARK: - happy path

    func testAQueryKnownPeerReturnsCorrectIP() {
        let query = aQuery(for: "h4.bamboo")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered for known *.bamboo A query")
            return
        }

        // Header sanity: same id, QR bit set, rcode=0 (NOERROR),
        // ancount >= 1.
        XCTAssertEqual(response[0], 0x42)
        XCTAssertEqual(response[1], 0x42)
        XCTAssertEqual(response[2] & 0x80, 0x80, "QR bit must be set")
        XCTAssertEqual(response[3] & 0x0F, 0x00, "rcode must be NOERROR")
        let ancount = (UInt16(response[6]) << 8) | UInt16(response[7])
        XCTAssertEqual(ancount, 1, "exactly one A record expected")

        // Last 4 bytes are the A record rdata (IP). Easier to check
        // by searching for the IP bytes than reverse-engineering the
        // answer-section offsets.
        let ipBytes: [UInt8] = [100, 64, 0, 3]
        XCTAssertTrue(response.suffix(4).elementsEqual(ipBytes),
                      "response should end with 100.64.0.3 as A rdata")
    }

    // MARK: - NXDOMAIN paths

    func testAQueryUnknownLabelReturnsNXDOMAIN() {
        let query = aQuery(for: "unknown-peer.bamboo")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered (with NXDOMAIN) for unknown label")
            return
        }

        XCTAssertEqual(response[3] & 0x0F, 0x03, "rcode must be NXDOMAIN (3)")
        let ancount = (UInt16(response[6]) << 8) | UInt16(response[7])
        XCTAssertEqual(ancount, 0, "NXDOMAIN must have zero answers")
    }

    func testApexBambooQueryReturnsNXDOMAIN() {
        // "bamboo." with no left label is treated as the apex zone —
        // SOA / NS would need a real answer; v1 chooses NXDOMAIN.
        let query = aQuery(for: "bamboo")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered (with NXDOMAIN) for apex")
            return
        }
        XCTAssertEqual(response[3] & 0x0F, 0x03)
    }

    // MARK: - AAAA semantics (NOERROR-empty for IPv4-only peer)

    func testAAAAQueryIPv4OnlyPeerReturnsNoErrorEmpty() {
        // mac-mini has ipv6=nil. AAAA should NOT NXDOMAIN — that
        // would poison the negative cache and the resolver would
        // stop asking for A. NOERROR + zero answers is the
        // "I exist, just not via this record type" signal.
        let query = aaaaQuery(for: "mac-mini.bamboo")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered (NOERROR-empty) for AAAA on IPv4-only peer")
            return
        }
        XCTAssertEqual(response[3] & 0x0F, 0x00,
                       "rcode must be NOERROR (0) — not NXDOMAIN")
        let ancount = (UInt16(response[6]) << 8) | UInt16(response[7])
        XCTAssertEqual(ancount, 0, "no AAAA answer expected for IPv4-only peer")
    }

    func testAAAAQueryDualStackPeerReturnsAAAARecord() {
        // h4 has ipv6="fd7a:115c:a1e0::3". AAAA should answer with
        // the synthesized record.
        let query = aaaaQuery(for: "h4.bamboo")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered with AAAA for dual-stack peer")
            return
        }
        XCTAssertEqual(response[3] & 0x0F, 0x00)
        let ancount = (UInt16(response[6]) << 8) | UInt16(response[7])
        XCTAssertEqual(ancount, 1, "expected one AAAA answer")
    }

    func testAAAAQueryUnknownPeerReturnsNXDOMAIN() {
        // Unknown label → NXDOMAIN regardless of qtype.
        let query = aaaaQuery(for: "unknown.bamboo")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered (NXDOMAIN) for unknown peer AAAA")
            return
        }
        XCTAssertEqual(response[3] & 0x0F, 0x03)
    }

    // MARK: - non-.bamboo → forward upstream

    func testNonBambooQueryReturnsForwardUpstream() {
        let query = aQuery(for: "www.google.com")

        guard case .forwardUpstream =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .forwardUpstream for non-*.bamboo query")
            return
        }
    }

    func testSimilarSuffixNotConfusedForBamboo() {
        // "x.bambooz" is NOT a *.bamboo name — should forward.
        let query = aQuery(for: "x.bambooz")

        guard case .forwardUpstream =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .forwardUpstream — must not match *.bambooz")
            return
        }
    }

    func testDeepSubdomainOfBambooStillResolvesByLabel() {
        // "deep.h4.bamboo" — the resolver strips the .bamboo suffix
        // and looks up the remaining label. "deep.h4" isn't in the
        // peer map. NXDOMAIN.
        let query = aQuery(for: "deep.h4.bamboo")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered (NXDOMAIN) for compound label")
            return
        }
        XCTAssertEqual(response[3] & 0x0F, 0x03)
    }

    // MARK: - case insensitivity

    func testCaseInsensitiveLabelMatch() {
        // Per RFC 1035 §2.3.3, DNS labels are ASCII-case-insensitive.
        // "H4.BAMBOO" must resolve to 100.64.0.3.
        let query = aQuery(for: "H4.BAMBOO")

        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered for uppercase variant")
            return
        }
        XCTAssertEqual(response[3] & 0x0F, 0x00, "rcode must be NOERROR")
        let ipBytes: [UInt8] = [100, 64, 0, 3]
        XCTAssertTrue(response.suffix(4).elementsEqual(ipBytes))
    }

    // MARK: - malformed input

    func testEmptyQueryReturnsMalformed() {
        guard case .malformed =
                MagicDNSResolver.handle(query: Data(), peers: peers) else {
            XCTFail("expected .malformed for empty query")
            return
        }
    }

    func testTruncatedHeaderReturnsMalformed() {
        // Only 5 bytes — header needs 12.
        let bad = Data([0x42, 0x42, 0x01, 0x00, 0x00])
        guard case .malformed =
                MagicDNSResolver.handle(query: bad, peers: peers) else {
            XCTFail("expected .malformed for truncated header")
            return
        }
    }

    func testMultiQuestionForwardsUpstream() {
        // qdcount=2 in header but only one question encoded. The
        // resolver short-circuits multi-question queries by handing
        // them off upstream rather than answering partially. The
        // exact behavior the code takes: parse will fail (no second
        // question after the first) → .malformed. Either malformed
        // or forwardUpstream is acceptable, but never .answered.
        var data = aQuery(for: "h4.bamboo")
        data[5] = 0x02  // qdcount = 2 (only one question follows)

        let outcome = MagicDNSResolver.handle(query: data, peers: peers)
        switch outcome {
        case .answered:
            XCTFail("multi-question must not be .answered — risk of half-answers")
        case .forwardUpstream, .malformed:
            break  // either is fine
        }
    }

    // MARK: - response trailers (TTL, etc)

    func testARecordResponseHasSensibleTTL() {
        let query = aQuery(for: "h4.bamboo")
        guard case .answered(let response) =
                MagicDNSResolver.handle(query: query, peers: peers) else {
            XCTFail("expected .answered")
            return
        }

        // Answer section starts after header (12) + question.
        // Question name = "\x02h4\x06bamboo\x00" = 11 bytes,
        // qtype+qclass = 4 bytes → question is 15 bytes.
        // Answer name (compressed pointer) is 2 bytes if compressed,
        // or echoed in full otherwise. The resolver re-encodes
        // uncompressed, so answer name = 11 bytes.
        // After answer name: type(2) + class(2) + TTL(4) + rdlen(2)
        //                  + rdata(4) = 14 bytes.
        let answerStart = 12 + 15  // = 27
        let nameLen = 11  // uncompressed "\x02h4\x06bamboo\x00"
        let ttlOffset = answerStart + nameLen + 4  // skip type+class
        XCTAssertLessThan(ttlOffset + 4, response.count,
                          "response shorter than expected; offset math wrong")

        let ttl = (UInt32(response[ttlOffset]) << 24)
                | (UInt32(response[ttlOffset+1]) << 16)
                | (UInt32(response[ttlOffset+2]) << 8)
                |  UInt32(response[ttlOffset+3])
        XCTAssertEqual(ttl, 60, "expected TTL=60 (resolver default)")
    }
}
