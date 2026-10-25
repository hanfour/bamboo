// SPDX-License-Identifier: Apache-2.0

import Foundation
import OSLog

/// What the tunnel extension needs to keep the device showing as online
/// while the host app is suspended. The app passes it inside
/// BambooTunnelConfig. Everything is optional upstream: a config written
/// by an older app has no presence block and the extension simply does not
/// heartbeat.
public struct PresenceConfig: Codable, Equatable {
    public let controllerURL: String
    public let tenantSlug: String
    public let peerID: String
    /// Peer-session bearer. It lives about 24h and the controller returns
    /// a replacement on a heartbeat inside the last 6h; PresenceHeartbeat
    /// keeps that replacement in memory so the extension stays authorized
    /// for as long as it runs.
    public let sessionToken: String

    public init(controllerURL: String, tenantSlug: String, peerID: String, sessionToken: String) {
        self.controllerURL = controllerURL
        self.tenantSlug = tenantSlug
        self.peerID = peerID
        self.sessionToken = sessionToken
    }
}

/// PresenceHeartbeat runs inside the packet-tunnel extension and POSTs
/// /api/v1/peers/heartbeat every 30s, for one reason only: iOS suspends
/// the host app in the background, which stops its own heartbeat, and the
/// controller then reports a connected device as offline. The extension
/// keeps running as long as the tunnel is up.
///
/// It sends no endpoints, bandwidth, or policy revision, so it changes
/// nothing on the controller except last_seen. Peer-list updates and relay
/// fallback still need the app in the foreground.
public actor PresenceHeartbeat {
    public static let interval: TimeInterval = 30

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app.tunnel", category: "presence")
    private let session: URLSession
    private let interval: TimeInterval
    private var task: Task<Void, Never>?
    private var config: PresenceConfig?

    public init(session: URLSession = .shared, interval: TimeInterval = PresenceHeartbeat.interval) {
        self.session = session
        self.interval = interval
    }

    /// Start, or replace the running loop when the identity changed. When
    /// only the token differs the loop keeps its cadence and just picks up
    /// the newer token.
    public func apply(_ new: PresenceConfig?) {
        guard let new else {
            stop()
            return
        }
        if let cur = config, task != nil,
           cur.peerID == new.peerID, cur.controllerURL == new.controllerURL, cur.tenantSlug == new.tenantSlug {
            config = new
            return
        }
        config = new
        task?.cancel()
        task = Task { [weak self] in
            while !Task.isCancelled {
                await self?.beat()
                try? await Task.sleep(nanoseconds: UInt64((self?.interval ?? 30) * 1_000_000_000))
            }
        }
    }

    public func stop() {
        task?.cancel()
        task = nil
        config = nil
    }

    /// One heartbeat. Returns true on a 2xx. Exposed for tests; the loop
    /// calls it. Errors are logged and tolerated.
    @discardableResult
    func beat() async -> Bool {
        guard let cfg = config, let base = URL(string: cfg.controllerURL) else { return false }
        var req = URLRequest(url: base.appendingPathComponent("/api/v1/peers/heartbeat"))
        req.httpMethod = "POST"
        req.timeoutInterval = 15
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("Bearer \(cfg.sessionToken)", forHTTPHeaderField: "Authorization")
        if !cfg.tenantSlug.isEmpty {
            req.setValue(cfg.tenantSlug, forHTTPHeaderField: "X-Tenant-Slug")
        }
        req.httpBody = try? JSONSerialization.data(withJSONObject: [
            "peerId": cfg.peerID,
            "knownPolicyRevision": 0,
        ])
        do {
            let (data, resp) = try await session.data(for: req)
            guard let http = resp as? HTTPURLResponse, 200..<300 ~= http.statusCode else {
                let code = (resp as? HTTPURLResponse)?.statusCode ?? -1
                log.warning("presence heartbeat: http \(code, privacy: .public)")
                return false
            }
            if let renewed = Self.renewedToken(in: data), renewed != cfg.sessionToken, config?.peerID == cfg.peerID {
                config = PresenceConfig(controllerURL: cfg.controllerURL, tenantSlug: cfg.tenantSlug,
                                        peerID: cfg.peerID, sessionToken: renewed)
                log.log("presence heartbeat: session token renewed")
            }
            return true
        } catch {
            log.warning("presence heartbeat: \(String(describing: error), privacy: .public)")
            return false
        }
    }

    /// The controller adds peerSessionToken to a heartbeat response only
    /// inside the renewal window.
    static func renewedToken(in data: Data) -> String? {
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let tok = obj["peerSessionToken"] as? String, !tok.isEmpty else { return nil }
        return tok
    }
}
