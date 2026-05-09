// SPDX-License-Identifier: Apache-2.0

import Foundation
import OSLog

/// PeerWatcher consumes the controller's
/// `GET /api/v1/peers/watch?peerId=...` Server-Sent Events stream and
/// emits decoded events to its delegate. Reconnects with exponential
/// backoff on stream breakage.
///
/// The controller emits four event names: peer_added, peer_updated,
/// peer_removed, policy_changed. We map each to a typed enum case so
/// callers don't have to parse raw JSON.
public actor PeerWatcher {
    public enum Event {
        case peerAdded(BambooClient.PeerJSON)
        case peerUpdated(BambooClient.PeerJSON)
        case peerRemoved(String)
        case policyChanged(Int64)
    }

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app", category: "watch")
    private var task: Task<Void, Never>?

    public init() {}

    public func start(baseURL: URL,
                      peerID: String,
                      bearerToken: String? = nil,
                      tenantSlug: String? = nil,
                      onEvent: @Sendable @escaping (Event) -> Void) {
        task?.cancel()
        let log = self.log

        task = Task {
            var backoff: TimeInterval = 1
            let maxBackoff: TimeInterval = 30

            while !Task.isCancelled {
                do {
                    var url = baseURL
                    url.appendPathComponent("api/v1/peers/watch")
                    var components = URLComponents(url: url, resolvingAgainstBaseURL: false)!
                    components.queryItems = [URLQueryItem(name: "peerId", value: peerID)]
                    var req = URLRequest(url: components.url!)
                    req.timeoutInterval = .infinity
                    if let tok = bearerToken, !tok.isEmpty {
                        req.setValue("Bearer \(tok)", forHTTPHeaderField: "Authorization")
                    }
                    if let slug = tenantSlug, !slug.isEmpty {
                        req.setValue(slug, forHTTPHeaderField: "X-Tenant-Slug")
                    }

                    let (bytes, response) = try await URLSession.shared.bytes(for: req)
                    if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
                        log.warning("watch: HTTP \(http.statusCode, privacy: .public); will retry")
                        try await Task.sleep(nanoseconds: UInt64(backoff * 1_000_000_000))
                        backoff = min(maxBackoff, backoff * 2)
                        continue
                    }
                    backoff = 1

                    var currentEvent: String = ""
                    for try await line in bytes.lines {
                        if Task.isCancelled { return }
                        if line.hasPrefix("event: ") {
                            currentEvent = String(line.dropFirst("event: ".count))
                        } else if line.hasPrefix("data: ") {
                            let payload = String(line.dropFirst("data: ".count))
                            if let event = try? PeerWatcher.decode(name: currentEvent, payload: payload) {
                                onEvent(event)
                            }
                        }
                        // Empty line is the SSE event boundary; the
                        // currentEvent fallback ensures back-to-back
                        // events with the same name still parse.
                    }
                } catch {
                    if Task.isCancelled { return }
                    log.warning("watch stream broken: \(String(describing: error), privacy: .public); backoff=\(backoff, privacy: .public)")
                    try? await Task.sleep(nanoseconds: UInt64(backoff * 1_000_000_000))
                    backoff = min(maxBackoff, backoff * 2)
                }
            }
        }
    }

    public func stop() {
        task?.cancel()
        task = nil
    }

    private static func decode(name: String, payload: String) throws -> Event {
        guard let data = payload.data(using: .utf8) else {
            throw DecodingError.dataCorrupted(.init(codingPath: [], debugDescription: "non-utf8 payload"))
        }
        switch name {
        case "peer_added":
            let body = try JSONDecoder().decode(PeerEnvelope.self, from: data)
            return .peerAdded(body.peer)
        case "peer_updated":
            let body = try JSONDecoder().decode(PeerEnvelope.self, from: data)
            return .peerUpdated(body.peer)
        case "peer_removed":
            let body = try JSONDecoder().decode(PeerIDEnvelope.self, from: data)
            return .peerRemoved(body.peerId)
        case "policy_changed":
            let body = try JSONDecoder().decode(PolicyEnvelope.self, from: data)
            return .policyChanged(body.policyRevision)
        default:
            throw DecodingError.dataCorrupted(.init(codingPath: [], debugDescription: "unknown event \(name)"))
        }
    }

    private struct PeerEnvelope: Decodable { let peer: BambooClient.PeerJSON }
    private struct PeerIDEnvelope: Decodable { let peerId: String }
    private struct PolicyEnvelope: Decodable { let policyRevision: Int64 }
}
