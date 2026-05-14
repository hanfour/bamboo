// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network
import OSLog

/// RelayClient is the Swift counterpart of `clients/core/relay`. It
/// owns one URLSessionWebSocketTask to a relay server and, for each
/// remote peer the WireGuard tunnel needs to talk to, a per-peer
/// localhost UDP NWConnection that:
///
///   - sends WG-inbound packets (received from the relay) to WG's
///     listening UDP port, with source = the per-peer local port,
///   - receives WG-outbound packets (WG dials this port as the peer's
///     Endpoint), and forwards them to the relay as PACKET frames.
public actor RelayClient {
    public enum Error: Swift.Error, CustomStringConvertible {
        case dialFailed(String)
        case unexpectedFrame(UInt8)
        case rejected(String)
        case closed

        public var description: String {
            switch self {
            case .dialFailed(let s):     return "RelayClient: dial failed: \(s)"
            case .unexpectedFrame(let t): return "RelayClient: unexpected frame 0x\(String(t, radix: 16))"
            case .rejected(let s):       return "RelayClient: rejected by relay: \(s)"
            case .closed:                return "RelayClient: closed"
            }
        }
    }

    public static let pubKeyLen = 32

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app", category: "relay")
    private var task: URLSessionWebSocketTask?
    private var readTask: Task<Void, Never>?

    private let selfKey: Data
    private let token: String
    private let wgListenAddr: NWEndpoint  // 127.0.0.1:<wg port>

    private var peers: [Data: PeerSocket] = [:]

    public init(selfKey: Data, token: String, wgListenHost: String, wgListenPort: UInt16) {
        precondition(selfKey.count == RelayClient.pubKeyLen, "selfKey must be 32 bytes")
        self.selfKey = selfKey
        self.token = token
        self.wgListenAddr = NWEndpoint.hostPort(
            host: NWEndpoint.Host(wgListenHost),
            port: NWEndpoint.Port(rawValue: wgListenPort)!
        )
    }

    /// Open a WSS session to relayURL, send CLIENT_HELLO, await
    /// SERVER_HELLO. Throws on any handshake failure.
    ///
    /// The relay server registers its WebSocket handler at `/relay`
    /// (see `apps/relay/cmd/relay/main.go`). Most users type just
    /// `wss://relay.example.com` into Settings and don't realise the
    /// path matters; the relay then returns a Caddy 404 and the WSS
    /// handshake fails with NSURLErrorBadServerResponse. Normalise
    /// here so the user-facing URL doesn't need the path.
    public func dial(relayURL: URL) async throws {
        let normalised: URL = {
            let path = relayURL.path
            if path.isEmpty || path == "/" {
                return relayURL.appendingPathComponent("relay")
            }
            return relayURL
        }()
        let session = URLSession(configuration: .ephemeral)
        let task = session.webSocketTask(with: normalised)
        task.resume()
        self.task = task

        // CLIENT_HELLO = 32-byte pubkey + token bytes.
        var helloPayload = Data()
        helloPayload.append(selfKey)
        helloPayload.append(Data(token.utf8))
        try await sendFrame(type: 0x02, payload: helloPayload)

        // SERVER_HELLO is the synchronous "registered" ack.
        let (typ, _) = try await readFrame()
        guard typ == 0x01 else {
            throw Error.unexpectedFrame(typ)
        }

        // Spawn the inbound reader.
        let me = self
        readTask = Task { await me.runReader() }
    }

    /// Add a peer; returns the localhost host:port string the
    /// WireGuard config should set as that peer's Endpoint.
    public func addPeer(_ peerKey: Data) async throws -> String {
        precondition(peerKey.count == RelayClient.pubKeyLen, "peerKey must be 32 bytes")
        if let existing = peers[peerKey] {
            return existing.endpointString()
        }

        // Bind a fresh ephemeral port on 127.0.0.1, "connected" to
        // WG's listen address. This NWConnection is bidirectional:
        // sending pushes packets to WG; receiving reads packets WG
        // sent here (where it thinks the peer endpoint is).
        let parameters = NWParameters.udp
        parameters.requiredLocalEndpoint = NWEndpoint.hostPort(
            host: NWEndpoint.Host("127.0.0.1"),
            port: NWEndpoint.Port.any
        )
        let conn = NWConnection(to: wgListenAddr, using: parameters)
        let ps = PeerSocket(pubkey: peerKey, conn: conn)
        peers[peerKey] = ps

        let queue = DispatchQueue(label: "dev.hanfour.bamboo.relay.peer")
        conn.stateUpdateHandler = { [weak self] state in
            if case .ready = state, let self = self {
                Task { await self.peerReady(ps) }
            }
        }
        conn.start(queue: queue)

        // Wait for ready so .currentPath has the assigned local port.
        for _ in 0..<50 {
            if case .ready = conn.state { break }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
        return ps.endpointString()
    }

    public func removePeer(_ peerKey: Data) {
        guard let ps = peers.removeValue(forKey: peerKey) else { return }
        ps.conn.cancel()
    }

    public func close() {
        readTask?.cancel()
        readTask = nil
        for ps in peers.values { ps.conn.cancel() }
        peers.removeAll()
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
    }

    // MARK: - private

    private func peerReady(_ ps: PeerSocket) {
        // Kick off the per-peer outbound reader.
        receiveOutbound(ps: ps)
    }

    private func receiveOutbound(ps: PeerSocket) {
        ps.conn.receiveMessage { [weak self] data, _, _, error in
            guard let self = self else { return }
            if let data = data, !data.isEmpty {
                Task { await self.forwardOutbound(ps: ps, body: data) }
            }
            if error == nil {
                self.receiveOutbound(ps: ps)
            }
        }
    }

    private func forwardOutbound(ps: PeerSocket, body: Data) async {
        var payload = Data()
        payload.append(ps.pubkey)
        payload.append(body)
        do {
            try await sendFrame(type: 0x03, payload: payload)
        } catch {
            log.warning("forwardOutbound: \(String(describing: error), privacy: .public)")
        }
    }

    private func runReader() async {
        while !Task.isCancelled {
            do {
                let (typ, payload) = try await readFrame()
                switch typ {
                case 0x03: // PACKET
                    if payload.count < RelayClient.pubKeyLen { continue }
                    let srcKey = payload.prefix(RelayClient.pubKeyLen)
                    let body = payload.dropFirst(RelayClient.pubKeyLen)
                    if let ps = peers[Data(srcKey)] {
                        ps.conn.send(content: Data(body), completion: .contentProcessed { _ in })
                    }
                case 0x04: // PEER_GONE
                    if payload.count >= 6 {
                        log.info("relay reports peer gone \(payload.prefix(6).map { String(format: "%02x", $0) }.joined(), privacy: .public)")
                    }
                case 0x05: // KEEPALIVE
                    break
                default:
                    break
                }
            } catch {
                if !Task.isCancelled {
                    log.warning("relay read: \(String(describing: error), privacy: .public)")
                }
                return
            }
        }
    }

    private func sendFrame(type: UInt8, payload: Data) async throws {
        guard let task = task else { throw Error.closed }
        let total = UInt32(4 + 1 + payload.count).bigEndian
        var buf = Data()
        withUnsafeBytes(of: total) { buf.append(contentsOf: $0) }
        buf.append(type)
        buf.append(payload)
        try await task.send(.data(buf))
    }

    private func readFrame() async throws -> (UInt8, Data) {
        guard let task = task else { throw Error.closed }
        let msg = try await task.receive()
        let raw: Data
        switch msg {
        case .data(let d): raw = d
        case .string(let s): raw = Data(s.utf8)
        @unknown default: throw Error.unexpectedFrame(0)
        }
        guard raw.count >= 5 else { throw Error.unexpectedFrame(0) }
        let declared = UInt32(raw[0]) << 24
                     | UInt32(raw[1]) << 16
                     | UInt32(raw[2]) << 8
                     |  UInt32(raw[3])
        guard Int(declared) == raw.count else { throw Error.unexpectedFrame(raw[4]) }
        return (raw[4], raw.dropFirst(5))
    }
}

private final class PeerSocket {
    let pubkey: Data
    let conn: NWConnection

    init(pubkey: Data, conn: NWConnection) {
        self.pubkey = pubkey
        self.conn = conn
    }

    func endpointString() -> String {
        if case let .hostPort(_, port) = conn.currentPath?.localEndpoint ?? .hostPort(host: "127.0.0.1", port: .any) {
            return "127.0.0.1:\(port.rawValue)"
        }
        return "127.0.0.1:0"
    }
}
