// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network

/// STUNClient is a tiny RFC 5389 client. It sends one binding request
/// to the configured STUN server, parses the XOR-MAPPED-ADDRESS in the
/// reply, and returns the host:port the bound socket appears at on
/// the public side. No long-lived sessions, no message integrity, no
/// failover — endpoint discovery for WireGuard handshakes does not
/// need any of it.
public enum STUNClient {
    /// Default Google STUN server. Reachable from most networks; replace
    /// in production deployments.
    public static let defaultServer = "stun.l.google.com:19302"

    public enum Error: Swift.Error, CustomStringConvertible {
        case dnsFailed(String)
        case writeFailed(Swift.Error?)
        case readFailed(Swift.Error?)
        case timeout
        case tooShort
        case badMagicCookie
        case transactionMismatch
        case unsupportedFamily(UInt8)
        case noMappedAddress

        public var description: String {
            switch self {
            case .dnsFailed(let s):              return "STUN: dns failed for \(s)"
            case .writeFailed(let e):            return "STUN: write failed: \(String(describing: e))"
            case .readFailed(let e):             return "STUN: read failed: \(String(describing: e))"
            case .timeout:                       return "STUN: timeout"
            case .tooShort:                      return "STUN: response too short"
            case .badMagicCookie:                return "STUN: bad magic cookie"
            case .transactionMismatch:           return "STUN: transaction id mismatch"
            case .unsupportedFamily(let f):      return "STUN: unsupported address family 0x\(String(f, radix: 16))"
            case .noMappedAddress:               return "STUN: no MAPPED-ADDRESS in response"
            }
        }
    }

    /// Discover sends a binding request to the STUN server (host:port)
    /// and returns the host:port the discovery socket presents to the
    /// outside world. timeoutSeconds applies to both the connect and
    /// read.
    ///
    /// localPort optionally pins the source UDP port the request goes
    /// out on. The default 0 lets the OS pick an ephemeral port; that
    /// works for discovery but the resulting external endpoint is keyed
    /// on a port the WireGuard socket never listens on, so other peers
    /// can't dial it. Callers that intend to advertise the discovered
    /// endpoint as the peer's reachable address should pass the same
    /// port WireGuard will listen on. See Finding #4 in
    /// docs/development/project-understanding-2026-05-13.md.
    public static func discover(server: String = defaultServer,
                                localPort: UInt16 = 0,
                                timeoutSeconds: Double = 2.0) async throws -> String {
        let parts = server.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2, let port = UInt16(parts[1]) else {
            throw Error.dnsFailed(server)
        }
        let host = NWEndpoint.Host(parts[0])
        let nwPort = NWEndpoint.Port(rawValue: port) ?? .any

        let queue = DispatchQueue(label: "dev.hanfour.bamboo.stun")
        let params: NWParameters = .udp
        if localPort != 0, let lp = NWEndpoint.Port(rawValue: localPort) {
            params.requiredLocalEndpoint = NWEndpoint.hostPort(host: .ipv4(.any), port: lp)
        }
        let conn = NWConnection(host: host, port: nwPort, using: params)

        let txID = generateTransactionID()
        let request = buildBindingRequest(transactionID: txID)

        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Swift.Error>) in
            conn.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    cont.resume()
                case .failed(let err):
                    cont.resume(throwing: Error.dnsFailed(String(describing: err)))
                case .cancelled:
                    break
                default:
                    break
                }
            }
            conn.start(queue: queue)
        }

        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Swift.Error>) in
            conn.send(content: request, completion: .contentProcessed { err in
                if let err = err {
                    cont.resume(throwing: Error.writeFailed(err))
                } else {
                    cont.resume()
                }
            })
        }

        let response: Data = try await withThrowingTaskGroup(of: Data.self) { group in
            group.addTask {
                return try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Data, Swift.Error>) in
                    conn.receiveMessage { (data, _, _, err) in
                        if let err = err {
                            cont.resume(throwing: Error.readFailed(err))
                            return
                        }
                        guard let data = data else {
                            cont.resume(throwing: Error.readFailed(nil))
                            return
                        }
                        cont.resume(returning: data)
                    }
                }
            }
            group.addTask {
                try await Task.sleep(nanoseconds: UInt64(timeoutSeconds * 1_000_000_000))
                throw Error.timeout
            }
            defer { group.cancelAll() }
            guard let first = try await group.next() else { throw Error.timeout }
            return first
        }
        conn.cancel()

        return try parseResponse(response, transactionID: txID)
    }

    // MARK: - encoding / decoding

    private static let magicCookie: UInt32 = 0x2112_A442
    private static let bindingRequestType: UInt16 = 0x0001
    private static let xorMappedAddress: UInt16 = 0x0020
    private static let mappedAddress: UInt16 = 0x0001

    private static func generateTransactionID() -> [UInt8] {
        var id = [UInt8](repeating: 0, count: 12)
        for i in 0..<id.count { id[i] = UInt8.random(in: 0...255) }
        return id
    }

    private static func buildBindingRequest(transactionID: [UInt8]) -> Data {
        var b = Data(capacity: 20)
        b.append(uint16: bindingRequestType)
        b.append(uint16: 0) // length: no attributes
        b.append(uint32: magicCookie)
        b.append(contentsOf: transactionID)
        return b
    }

    private static func parseResponse(_ buf: Data, transactionID: [UInt8]) throws -> String {
        guard buf.count >= 20 else { throw Error.tooShort }

        let cookie = buf.uint32(at: 4)
        guard cookie == magicCookie else { throw Error.badMagicCookie }

        let receivedTx = Array(buf[8..<20])
        guard receivedTx == transactionID else { throw Error.transactionMismatch }

        let declaredLen = Int(buf.uint16(at: 2))
        guard 20 + declaredLen <= buf.count else { throw Error.tooShort }

        var attrs = buf[20..<(20 + declaredLen)]
        while attrs.count >= 4 {
            let atype = attrs.uint16(at: attrs.startIndex)
            let alen = Int(attrs.uint16(at: attrs.startIndex + 2))
            guard 4 + alen <= attrs.count else { throw Error.tooShort }
            let bodyStart = attrs.startIndex + 4
            let body = attrs[bodyStart..<(bodyStart + alen)]
            switch atype {
            case xorMappedAddress:
                return try parseXORMappedAddress(body, transactionID: transactionID)
            case mappedAddress:
                return try parsePlainMappedAddress(body)
            default:
                break
            }
            // 4-byte alignment between TLVs.
            var step = 4 + alen
            if step % 4 != 0 { step += 4 - (step % 4) }
            if step >= attrs.count { break }
            attrs = attrs[(attrs.startIndex + step)...]
        }
        throw Error.noMappedAddress
    }

    private static func parseXORMappedAddress(_ body: Data.SubSequence, transactionID: [UInt8]) throws -> String {
        guard body.count >= 8 else { throw Error.tooShort }
        let family = body[body.startIndex + 1]
        let portXOR = body.uint16(at: body.startIndex + 2)
        let port = portXOR ^ UInt16(magicCookie >> 16)

        switch family {
        case 0x01:
            var bytes = [UInt8](repeating: 0, count: 4)
            var cookie = [UInt8](repeating: 0, count: 4)
            cookie[0] = UInt8((magicCookie >> 24) & 0xFF)
            cookie[1] = UInt8((magicCookie >> 16) & 0xFF)
            cookie[2] = UInt8((magicCookie >> 8) & 0xFF)
            cookie[3] = UInt8(magicCookie & 0xFF)
            for i in 0..<4 {
                bytes[i] = body[body.startIndex + 4 + i] ^ cookie[i]
            }
            return "\(bytes[0]).\(bytes[1]).\(bytes[2]).\(bytes[3]):\(port)"
        case 0x02:
            guard body.count >= 20 else { throw Error.tooShort }
            var key = [UInt8](repeating: 0, count: 16)
            key[0] = UInt8((magicCookie >> 24) & 0xFF)
            key[1] = UInt8((magicCookie >> 16) & 0xFF)
            key[2] = UInt8((magicCookie >> 8) & 0xFF)
            key[3] = UInt8(magicCookie & 0xFF)
            for i in 0..<12 { key[4 + i] = transactionID[i] }
            var bytes = [UInt8](repeating: 0, count: 16)
            for i in 0..<16 { bytes[i] = body[body.startIndex + 4 + i] ^ key[i] }
            return "[\(formatIPv6(bytes))]:\(port)"
        default:
            throw Error.unsupportedFamily(family)
        }
    }

    private static func parsePlainMappedAddress(_ body: Data.SubSequence) throws -> String {
        guard body.count >= 8 else { throw Error.tooShort }
        let family = body[body.startIndex + 1]
        let port = body.uint16(at: body.startIndex + 2)
        switch family {
        case 0x01:
            return "\(body[body.startIndex + 4]).\(body[body.startIndex + 5]).\(body[body.startIndex + 6]).\(body[body.startIndex + 7]):\(port)"
        case 0x02:
            guard body.count >= 20 else { throw Error.tooShort }
            var bytes = [UInt8](repeating: 0, count: 16)
            for i in 0..<16 { bytes[i] = body[body.startIndex + 4 + i] }
            return "[\(formatIPv6(bytes))]:\(port)"
        default:
            throw Error.unsupportedFamily(family)
        }
    }

    private static func formatIPv6(_ bytes: [UInt8]) -> String {
        var groups: [String] = []
        for i in stride(from: 0, to: 16, by: 2) {
            let value = (UInt16(bytes[i]) << 8) | UInt16(bytes[i + 1])
            groups.append(String(value, radix: 16))
        }
        return groups.joined(separator: ":")
    }
}

private extension Data {
    mutating func append(uint16 value: UInt16) {
        let be = value.bigEndian
        var v = be
        Swift.withUnsafeBytes(of: &v) { self.append(contentsOf: $0) }
    }

    mutating func append(uint32 value: UInt32) {
        let be = value.bigEndian
        var v = be
        Swift.withUnsafeBytes(of: &v) { self.append(contentsOf: $0) }
    }

    func uint16(at offset: Index) -> UInt16 {
        return (UInt16(self[offset]) << 8) | UInt16(self[offset + 1])
    }

    func uint32(at offset: Index) -> UInt32 {
        return (UInt32(self[offset]) << 24)
             | (UInt32(self[offset + 1]) << 16)
             | (UInt32(self[offset + 2]) << 8)
             |  UInt32(self[offset + 3])
    }
}
