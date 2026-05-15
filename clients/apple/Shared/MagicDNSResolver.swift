// SPDX-License-Identifier: Apache-2.0

import Foundation

/// MagicDNSResolver is a minimal RFC 1035 DNS message parser +
/// response builder, scoped to what bamboo's MagicDNS needs:
///
///   * parse a single UDP DNS query (one question per packet — the
///     standard shape; queries-with-multiple-questions are legal
///     but vanishingly rare in practice and we reject them
///     cleanly with FORMERR rather than try to handle them).
///   * synthesize an answer for `*.bamboo` names. A records pull
///     from the peer map; AAAA records return NOERROR with empty
///     answer (bamboo IPs are IPv4-only, but returning NXDOMAIN
///     here would make some resolvers cache the negative and
///     suppress subsequent A queries — NOERROR-empty is the
///     idiomatic "no AAAA, look elsewhere" response).
///   * synthesize NXDOMAIN for unknown `*.bamboo` labels.
///   * signal `nil` for non-`.bamboo` queries so the caller knows
///     to forward upstream.
///
/// The parser is intentionally strict and small. Pointer
/// compression in the question section is handled because real
/// resolvers occasionally emit it; the answer section
/// (synthesized here) uses uncompressed names so we never need to
/// build a compressed name.
///
/// No dependencies on Foundation niceties (Data subscripts only;
/// no Codable / regex) so this code compiles equally in the host
/// app and the extension binary.
public enum MagicDNSResolver {

    /// Suffix the resolver claims. Includes a trailing dot since
    /// RFC 1035 names are always rooted. The match is also done
    /// against the unrooted form ("bamboo") to handle resolvers
    /// that strip the trailing dot before delivering.
    public static let zone = "bamboo"

    /// Top-level entry. Given a raw DNS query datagram + the
    /// current peer map, returns:
    ///   - .answered(Data): a synthesized response to write back
    ///     to the flow.
    ///   - .forwardUpstream: the query is not for `.bamboo`; the
    ///     caller should proxy it to the OS's default resolver.
    ///   - .malformed: the query couldn't even be parsed; the
    ///     caller can close the flow without writing.
    public enum Outcome {
        case answered(Data)
        case forwardUpstream
        case malformed
    }

    public static func handle(query: Data,
                              peers: [String: MagicDNSPeerStore.PeerEntry]) -> Outcome {
        guard let msg = DNSMessage.parse(query) else {
            return .malformed
        }
        // Only standard queries with exactly one question are
        // supported. Anything else: we don't pretend, we don't
        // forward (FORMERR would be the strict answer, but the
        // simplest safe move is to bow out and let upstream try).
        guard msg.questions.count == 1, msg.opcode == 0 else {
            return .forwardUpstream
        }
        let q = msg.questions[0]
        let lowerName = q.name.lowercased()
        let stripped = lowerName.hasSuffix(".") ? String(lowerName.dropLast()) : lowerName
        let suffix = "." + zone
        guard stripped.hasSuffix(suffix) || stripped == zone else {
            return .forwardUpstream
        }
        // Extract the label: "mac-mini.bamboo" → "mac-mini".
        let label: String
        if stripped == zone {
            label = "" // bare ".bamboo" — no peer matches.
        } else {
            label = String(stripped.dropLast(suffix.count))
        }
        // Apex-zone query (e.g. SOA "bamboo.") — return NXDOMAIN
        // for now. A future PR can synthesize an SOA so the apex
        // doesn't generate negative caches; for v1 the simpler
        // answer is fine.
        if label.isEmpty {
            return .answered(DNSMessage.nxdomain(for: msg))
        }
        guard let entry = peers[label] else {
            return .answered(DNSMessage.nxdomain(for: msg))
        }
        switch q.qtype {
        case .a:
            guard let bytes = ipv4Bytes(entry.ipv4) else {
                return .answered(DNSMessage.nxdomain(for: msg))
            }
            return .answered(DNSMessage.aRecord(for: msg, ipv4: bytes))
        case .aaaa:
            if let ipv6 = entry.ipv6, let bytes = ipv6Bytes(ipv6) {
                return .answered(DNSMessage.aaaaRecord(for: msg, ipv6: bytes))
            }
            // No IPv6 for this peer — NOERROR + empty answer so
            // the resolver doesn't negative-cache the whole name.
            return .answered(DNSMessage.noerrorEmpty(for: msg))
        default:
            // Type we don't synthesize (e.g. MX, TXT). Return
            // NOERROR + empty so the resolver tolerates and
            // doesn't poison its A cache.
            return .answered(DNSMessage.noerrorEmpty(for: msg))
        }
    }

    // MARK: - IP parsing

    static func ipv4Bytes(_ s: String) -> [UInt8]? {
        let parts = s.split(separator: ".")
        guard parts.count == 4 else { return nil }
        var out: [UInt8] = []
        for p in parts {
            guard let n = UInt8(p) else { return nil }
            out.append(n)
        }
        return out
    }

    /// Parse a textual IPv6 (with optional "::" compression).
    /// Returns 16 bytes in network order.
    static func ipv6Bytes(_ s: String) -> [UInt8]? {
        var addr = in6_addr()
        let ok = s.withCString { ptr in
            inet_pton(AF_INET6, ptr, &addr) == 1
        }
        guard ok else { return nil }
        return withUnsafeBytes(of: &addr) { raw in
            Array(raw.bindMemory(to: UInt8.self))
        }
    }
}

// MARK: - DNS message

/// DNSMessage is the parsed shape of a query. We only model the
/// header and the question section; queries don't carry answer /
/// authority / additional sections in practice and we don't need
/// to round-trip them.
public struct DNSMessage {
    public let id: UInt16
    /// Raw flag word (header octets 2-3 big-endian). Used to copy
    /// the opcode + RD bit into the response.
    public let flags: UInt16
    public let questions: [Question]

    public var opcode: UInt8 { UInt8((flags >> 11) & 0x0F) }
    public var rdBit: UInt16 { flags & 0x0100 }

    public struct Question {
        public let name: String  // canonical form "label.label.", trailing dot
        public let qtype: QType
        public let qclass: UInt16
    }

    public enum QType: UInt16 {
        case a = 1
        case aaaa = 28
        case cname = 5
        case ptr = 12
        case mx = 15
        case txt = 16
        case soa = 6
        case ns = 2
        case other = 0  // sentinel
    }

    public static func parse(_ data: Data) -> DNSMessage? {
        guard data.count >= 12 else { return nil }
        let id = data.readUInt16(at: 0)
        let flags = data.readUInt16(at: 2)
        let qdcount = data.readUInt16(at: 4)
        // We don't parse an/ns/ar in queries — they're always
        // zero-or-irrelevant. Track offset for the question
        // section only.
        var offset = 12
        var questions: [Question] = []
        for _ in 0..<Int(qdcount) {
            guard let (name, nextOffset) = decodeName(data, at: offset) else {
                return nil
            }
            offset = nextOffset
            guard offset + 4 <= data.count else { return nil }
            let typeRaw = data.readUInt16(at: offset)
            let cls = data.readUInt16(at: offset + 2)
            offset += 4
            let qt = QType(rawValue: typeRaw) ?? .other
            questions.append(Question(name: name, qtype: qt, qclass: cls))
        }
        return DNSMessage(id: id, flags: flags, questions: questions)
    }

    /// decodeName reads a domain name starting at `offset`,
    /// resolving pointer-compression jumps as needed. Returns
    /// (decoded name, offset AFTER the name in the original
    /// buffer — not the jump target). Strict guards prevent
    /// infinite pointer loops + reading past the end.
    static func decodeName(_ data: Data, at start: Int) -> (String, Int)? {
        var labels: [String] = []
        var i = start
        var jumpedReturnOffset: Int? = nil
        var jumpCount = 0
        while i < data.count {
            let lenByte = data[i]
            if lenByte == 0 {
                // Root label — terminator. Advance one byte unless
                // we already jumped, in which case the returned
                // offset is the one we saved before the jump.
                let end = jumpedReturnOffset ?? (i + 1)
                let name = labels.isEmpty ? "." : labels.joined(separator: ".") + "."
                return (name, end)
            }
            if (lenByte & 0xC0) == 0xC0 {
                // Pointer. 14-bit offset into the message.
                guard i + 1 < data.count else { return nil }
                let high = UInt16(lenByte & 0x3F) << 8
                let low = UInt16(data[i + 1])
                let pointerTarget = Int(high | low)
                if jumpedReturnOffset == nil {
                    jumpedReturnOffset = i + 2
                }
                jumpCount += 1
                if jumpCount > 16 || pointerTarget >= data.count {
                    return nil  // defend against pointer cycles
                }
                i = pointerTarget
                continue
            }
            // Standard label.
            let labelLen = Int(lenByte)
            let labelStart = i + 1
            let labelEnd = labelStart + labelLen
            guard labelEnd <= data.count else { return nil }
            let bytes = data.subdata(in: labelStart..<labelEnd)
            guard let label = String(data: bytes, encoding: .ascii) else { return nil }
            labels.append(label)
            i = labelEnd
        }
        return nil
    }

    // MARK: - Response builders

    /// Build a NOERROR + empty answer response. Used for AAAA
    /// queries against IPv4-only peers and for record types we
    /// don't synthesize (MX/TXT/...).
    public static func noerrorEmpty(for query: DNSMessage) -> Data {
        build(reply: query, rcode: 0, answers: [])
    }

    /// Build an NXDOMAIN response.
    public static func nxdomain(for query: DNSMessage) -> Data {
        build(reply: query, rcode: 3, answers: [])
    }

    /// Build a response with a single A record. ipv4Bytes must be
    /// 4 elements.
    public static func aRecord(for query: DNSMessage,
                               ipv4: [UInt8],
                               ttlSeconds: UInt32 = 60) -> Data {
        precondition(ipv4.count == 4, "ipv4 must be 4 bytes")
        let answer = Answer(name: query.questions[0].name,
                            type: 1,
                            cls: 1,
                            ttl: ttlSeconds,
                            rdata: Data(ipv4))
        return build(reply: query, rcode: 0, answers: [answer])
    }

    /// Build a response with a single AAAA record. ipv6Bytes must
    /// be 16 elements.
    public static func aaaaRecord(for query: DNSMessage,
                                  ipv6: [UInt8],
                                  ttlSeconds: UInt32 = 60) -> Data {
        precondition(ipv6.count == 16, "ipv6 must be 16 bytes")
        let answer = Answer(name: query.questions[0].name,
                            type: 28,
                            cls: 1,
                            ttl: ttlSeconds,
                            rdata: Data(ipv6))
        return build(reply: query, rcode: 0, answers: [answer])
    }

    struct Answer {
        let name: String
        let type: UInt16
        let cls: UInt16
        let ttl: UInt32
        let rdata: Data
    }

    private static func build(reply query: DNSMessage,
                              rcode: UInt8,
                              answers: [Answer]) -> Data {
        var out = Data()
        out.appendUInt16(query.id)
        // Build flags: QR=1, Opcode=copy from query, AA=1, TC=0,
        // RD=copy from query, RA=1, Z=0, RCODE=rcode.
        let qrBit: UInt16 = 0x8000
        let opcodeMask: UInt16 = 0x7800
        let aaBit: UInt16 = 0x0400
        let raBit: UInt16 = 0x0080
        let respFlags = qrBit
                      | (query.flags & opcodeMask)
                      | aaBit
                      | query.rdBit
                      | raBit
                      | UInt16(rcode & 0x0F)
        out.appendUInt16(respFlags)
        out.appendUInt16(UInt16(query.questions.count))
        out.appendUInt16(UInt16(answers.count))
        out.appendUInt16(0)
        out.appendUInt16(0)
        // Question section. Re-encode rather than copy from the
        // original query bytes — the input may have used pointer
        // compression and re-emitting it requires more bookkeeping
        // than a straight encode.
        for q in query.questions {
            out.append(encodeName(q.name))
            out.appendUInt16(q.qtype.rawValue == 0 ? 0 : q.qtype.rawValue)
            out.appendUInt16(q.qclass)
        }
        // Answer section.
        for a in answers {
            out.append(encodeName(a.name))
            out.appendUInt16(a.type)
            out.appendUInt16(a.cls)
            out.appendUInt32(a.ttl)
            out.appendUInt16(UInt16(a.rdata.count))
            out.append(a.rdata)
        }
        return out
    }

    /// encodeName takes a canonical name like "mac-mini.bamboo."
    /// and produces the wire-format byte sequence ending in a
    /// 0x00 terminator. Inputs without a trailing dot are accepted
    /// and treated as already-rooted (we tack on the terminator
    /// either way).
    static func encodeName(_ name: String) -> Data {
        var out = Data()
        if name == "." || name.isEmpty {
            out.append(0)
            return out
        }
        let trimmed = name.hasSuffix(".") ? String(name.dropLast()) : name
        for label in trimmed.split(separator: ".", omittingEmptySubsequences: true) {
            let bytes = Array(label.utf8)
            if bytes.count == 0 || bytes.count > 63 { continue }
            out.append(UInt8(bytes.count))
            out.append(contentsOf: bytes)
        }
        out.append(0)
        return out
    }
}

// MARK: - Data helpers (private to this file)

extension Data {
    fileprivate func readUInt16(at offset: Int) -> UInt16 {
        let hi = UInt16(self[offset])
        let lo = UInt16(self[offset + 1])
        return (hi << 8) | lo
    }

    fileprivate mutating func appendUInt16(_ value: UInt16) {
        append(UInt8(value >> 8))
        append(UInt8(value & 0xFF))
    }

    fileprivate mutating func appendUInt32(_ value: UInt32) {
        append(UInt8((value >> 24) & 0xFF))
        append(UInt8((value >> 16) & 0xFF))
        append(UInt8((value >> 8) & 0xFF))
        append(UInt8(value & 0xFF))
    }
}
