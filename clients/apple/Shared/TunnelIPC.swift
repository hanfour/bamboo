// SPDX-License-Identifier: Apache-2.0

import Foundation

/// TunnelIPC defines the message envelope the host app and the
/// PacketTunnelProvider extension exchange via
/// NETunnelProviderSession.sendProviderMessage / handleAppMessage.
///
/// The wire format is JSON (small messages, easy to debug). Each
/// message has a `kind` discriminator and an optional payload — when
/// adding new kinds, keep payloads forward-compatible (extensions may
/// be older than the host app on iOS where TestFlight ships them
/// independently).
public enum TunnelIPC {
    public enum Kind: String, Codable {
        /// The host app pushes a refreshed BambooTunnelConfig. The
        /// extension applies it via WireGuardAdapter.update without
        /// dropping the tunnel.
        case applyConfig

        /// The host app asks the extension for current runtime status.
        /// Useful for "show last handshake time" UI; extension replies
        /// with a status payload.
        case status
    }

    /// Request envelope sent app -> extension.
    public struct Request: Codable {
        public let kind: Kind
        public let config: BambooTunnelConfig?

        public init(kind: Kind, config: BambooTunnelConfig? = nil) {
            self.kind = kind
            self.config = config
        }
    }

    /// Response envelope sent extension -> app. `ok=false` plus a
    /// non-empty `error` indicates failure; `ok=true` may carry an
    /// optional status payload.
    public struct Response: Codable {
        public let ok: Bool
        public let error: String?
        public let status: TunnelStatus?

        public init(ok: Bool, error: String? = nil, status: TunnelStatus? = nil) {
            self.ok = ok
            self.error = error
            self.status = status
        }
    }

    /// TunnelStatus is what `kind=status` returns. Currently a thin
    /// envelope; will grow per-peer handshake info over time.
    public struct TunnelStatus: Codable {
        public let connected: Bool
        public let peerCount: Int

        public init(connected: Bool, peerCount: Int) {
            self.connected = connected
            self.peerCount = peerCount
        }
    }

    public static func encode(_ req: Request) throws -> Data {
        return try JSONEncoder().encode(req)
    }

    public static func decode(_ data: Data) throws -> Request {
        return try JSONDecoder().decode(Request.self, from: data)
    }

    public static func encode(_ resp: Response) throws -> Data {
        return try JSONEncoder().encode(resp)
    }

    public static func decodeResponse(_ data: Data) throws -> Response {
        return try JSONDecoder().decode(Response.self, from: data)
    }
}
