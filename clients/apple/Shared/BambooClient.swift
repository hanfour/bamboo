// SPDX-License-Identifier: Apache-2.0

import Foundation

/// BambooClient is the minimal REST surface the macOS / iOS app uses
/// to talk to the bamboo controller. The shape of every method matches
/// the controller's JSON wire format under apps/controller/internal/server/.
///
/// Authentication precedence depends on the endpoint:
///
///   - Peer-bound endpoints (heartbeat / watch / relay-token mint /
///     re-register) prefer `peerSessionToken` — the controller-issued
///     bearer received in RegisterResponse. Once prod-mode auth is on
///     (BAMBOO_REQUIRE_AUTH=true) this is the only credential that
///     keeps these calls working.
///   - User-bound endpoints (`/me`) use `bearerToken` — the OIDC
///     session JWT.
///   - Register itself accepts pre-auth-key in the body OR either
///     bearer above.
///   - X-Tenant-Slug header is the dev fallback; the controller
///     rejects it for peer-bound endpoints under prod mode.
public struct BambooClient {
    public let baseURL: URL
    public var bearerToken: String?
    /// Peer-bound bearer minted by the controller on Register. Sent on
    /// heartbeat / watch / relay-token / re-register; absent until the
    /// first successful register completes. The struct is a value
    /// type, so consumers receive a fresh BambooClient with this
    /// field set via `withPeerSessionToken(_:)`.
    public var peerSessionToken: String?
    public var tenantSlug: String?

    public init(baseURL: URL,
                bearerToken: String? = nil,
                peerSessionToken: String? = nil,
                tenantSlug: String? = nil) {
        self.baseURL = baseURL
        self.bearerToken = bearerToken
        self.peerSessionToken = peerSessionToken
        self.tenantSlug = tenantSlug
    }

    /// withPeerSessionToken returns a copy of this client with the
    /// peer-bound bearer set. Used by ConnectionViewModel right after
    /// register() returns the controller-issued token.
    public func withPeerSessionToken(_ token: String?) -> BambooClient {
        var copy = self
        copy.peerSessionToken = token
        return copy
    }

    public struct RegisterRequest: Encodable {
        public var hostname: String
        public var wireguardPublicKey: String
        public var os: String
        public var clientVersion: String
        public var preAuthKeySecret: String?
        public var tenantSlug: String?
        public var endpoints: [String]?
        /// CIDRs the peer wants to expose to the tailnet (issue #136
        /// subnet router). Omitted when nil so legacy deployments
        /// without #136 columns see the unchanged wire shape; the
        /// admin still must approve a subset before they merge into
        /// other peers' allowed_ips.
        public var advertisedRoutes: [String]?
        /// True when the peer wants to be considered as an exit-node
        /// candidate (issue #137). Omitted when nil — same legacy
        /// argument as advertisedRoutes; the admin still has to flip
        /// `exit_node_approved` server-side before any peer routes
        /// its default through this one.
        public var advertiseExitNode: Bool?
    }

    public struct PeerJSON: Codable, Equatable {
        public var id: String
        public var tenantId: String
        public var hostname: String
        public var ip: String
        public var wireguardPublicKey: String
        public var os: String?
        public var clientVersion: String?
        public var endpoints: [String]?
        /// peerDnsName is the short DNS-safe label (e.g. "mac-mini")
        /// under which this peer resolves to its tunnel IP via
        /// MagicDNS — controller-side auto-slugified from hostname,
        /// or admin-renamed. Optional: legacy controllers / peers
        /// registered before the column existed omit it.
        public var peerDnsName: String?
        /// allowedIps is the controller-computed L3 reachability set
        /// (issue #132). Only populated on Register responses; watch
        /// events carry an absent allowedIps and the recipient must
        /// preserve the prior register-time value in a side cache. An
        /// empty array under PolicyRevision > 0 means "policy denies"
        /// and the peer should be omitted from the WireGuard config
        /// entirely; under PolicyRevision == 0 (no policy authored)
        /// the client falls back to a /32 per peer.
        public var allowedIps: [String]?
    }

    /// RelayServer is one entry in the controller-curated relay
    /// list (§4 P2 multi-relay registry). Stage 1's reaper already
    /// filtered out unhealthy rows so the Apple client can pick
    /// any entry without re-checking health. The client uses these
    /// to RTT-probe and pick the lowest-latency relay when the
    /// user hasn't pinned one via Settings (`relayURL`).
    public struct RelayServer: Decodable, Equatable {
        public var id: String
        public var region: String
        public var hostname: String
        public var port: Int
        public var publicKey: String

        enum CodingKeys: String, CodingKey {
            case id, region, hostname, port
            case publicKey = "publicKey"
        }
    }

    public struct RegisterResponse: Decodable {
        public var self_: PeerJSON
        public var peers: [PeerJSON]
        public var policyRevision: Int64
        /// Controller-issued peer-bound bearer (HMAC, ~24h TTL).
        /// Optional in the wire shape: an older controller that
        /// predates the prod-mode auth train omits these fields.
        public var peerSessionToken: String?
        public var peerSessionExpiresAt: Int64?
        /// Curated eligible relay list (§4 P2 multi-relay stage 2).
        /// Optional so an older controller without the registry
        /// still decodes — empty / missing ⇒ direct-only mesh
        /// unless the user typed a relayURL in Settings.
        public var relayServers: [RelayServer]?
        /// Controller-inferred region hint (§4 P2 multi-relay
        /// stage 5.5b). Set when the operator configured
        /// BAMBOO_REGION_CIDRS on the controller AND our request
        /// IP matched an entry. Local Settings hint wins —
        /// this is the fleet-wide default for clients that
        /// didn't pin one themselves. Optional / nilable so a
        /// controller without stage 5.5a still decodes.
        public var preferredRegion: String?

        enum CodingKeys: String, CodingKey {
            case self_ = "self"
            case peers, policyRevision, peerSessionToken, peerSessionExpiresAt, relayServers, preferredRegion
        }
    }

    public struct MeResponse: Decodable {
        public var authenticated: Bool
        public var userId: String?
        public var email: String?
        public var displayName: String?
        public var tenantId: String
        public var tenantSlug: String
    }

    public struct HeartbeatRequest: Encodable {
        public var peerId: String
        public var knownPolicyRevision: Int64
        public var endpoints: [String]?
        /// Cumulative wg byte counters summed across every peer on
        /// the local interface (§4 P2). Both nil ⇒ no data this
        /// tick; the controller skips its bandwidth_sample write.
        /// Optional so older extension builds (without IPC
        /// `bandwidthStats`) keep producing a valid request.
        public var bytesSent: UInt64?
        public var bytesReceived: UInt64?

        public init(peerId: String,
                    knownPolicyRevision: Int64,
                    endpoints: [String]? = nil,
                    bytesSent: UInt64? = nil,
                    bytesReceived: UInt64? = nil) {
            self.peerId = peerId
            self.knownPolicyRevision = knownPolicyRevision
            self.endpoints = endpoints
            self.bytesSent = bytesSent
            self.bytesReceived = bytesReceived
        }
    }

    public struct HeartbeatResponse: Decodable {
        public var peersChanged: Bool
        public var policyChanged: Bool
        public var currentPolicyRevision: Int64
    }

    public struct RelayTokenRequest: Encodable {
        public var peerId: String
        public var wireguardPublicKey: String
    }

    public struct RelayTokenResponse: Decodable {
        public var token: String
        public var expiresAt: Date?
    }

    public func register(_ req: RegisterRequest) async throws -> RegisterResponse {
        // Register is the credential bootstrap: either a pre-auth-key
        // lives on the body, or a user-session Bearer authenticates an
        // admin-driven mint. Peer-session Bearer is valid too on
        // re-register, so prefer it when present.
        return try await postJSON("/api/v1/peers/register", req, peerBound: true)
    }

    public func heartbeat(_ req: HeartbeatRequest) async throws -> HeartbeatResponse {
        return try await postJSON("/api/v1/peers/heartbeat", req, peerBound: true)
    }

    public func me() async throws -> MeResponse {
        // /me is strictly user-session — the controller looks up the
        // user by claims.UserID, and a peer-session token has none.
        return try await getJSON("/api/v1/me", peerBound: false)
    }

    public func relayToken(_ req: RelayTokenRequest) async throws -> RelayTokenResponse {
        return try await postJSON("/api/v1/relay-token", req, peerBound: true)
    }

    // MARK: - private

    private func getJSON<T: Decodable>(_ path: String, peerBound: Bool) async throws -> T {
        var req = URLRequest(url: baseURL.appendingPathComponent(path))
        req.httpMethod = "GET"
        applyHeaders(to: &req, peerBound: peerBound)
        return try await sendAndDecode(req)
    }

    private func postJSON<Req: Encodable, Resp: Decodable>(_ path: String, _ body: Req, peerBound: Bool) async throws -> Resp {
        var req = URLRequest(url: baseURL.appendingPathComponent(path))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        applyHeaders(to: &req, peerBound: peerBound)
        req.httpBody = try JSONEncoder().encode(body)
        return try await sendAndDecode(req)
    }

    /// applyHeaders writes the Authorization + X-Tenant-Slug headers.
    /// For peer-bound endpoints the peer-session bearer is preferred;
    /// for user-bound endpoints only the OIDC user-session bearer is
    /// sent. The slug header is always written when set — the
    /// controller's prod-mode gate ignores it for peer-bound paths
    /// but dev mode still relies on it.
    private func applyHeaders(to req: inout URLRequest, peerBound: Bool) {
        let chosen: String?
        if peerBound {
            chosen = peerSessionToken ?? bearerToken
        } else {
            chosen = bearerToken
        }
        if let tok = chosen, !tok.isEmpty {
            req.setValue("Bearer \(tok)", forHTTPHeaderField: "Authorization")
        }
        if let slug = tenantSlug, !slug.isEmpty {
            req.setValue(slug, forHTTPHeaderField: "X-Tenant-Slug")
        }
    }

    private func sendAndDecode<T: Decodable>(_ req: URLRequest) async throws -> T {
        let (data, resp) = try await URLSession.shared.data(for: req)
        guard let http = resp as? HTTPURLResponse else {
            throw BambooClientError.invalidResponse
        }
        guard 200..<300 ~= http.statusCode else {
            let body = String(data: data, encoding: .utf8) ?? ""
            throw BambooClientError.httpStatus(code: http.statusCode, body: body)
        }
        return try Self.jsonDecoder.decode(T.self, from: data)
    }

    // The controller marshals Go time.Time as RFC3339Nano (a string
    // with fractional seconds, e.g. "2026-05-14T14:31:57.272Z").
    // Swift's `.iso8601` strategy uses an ISO8601DateFormatter that, by
    // default, rejects fractional seconds — a "valid" ISO8601 string
    // still throws `dataCorrupted("Expected date string to be ISO8601-
    // formatted.")`. Use a custom strategy that tries
    // `withFractionalSeconds` first and falls back to the no-fraction
    // form so we accept both shapes regardless of which Go version is
    // marshalling on the server side.
    private static let jsonDecoder: JSONDecoder = {
        let d = JSONDecoder()
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let noFraction = ISO8601DateFormatter()
        noFraction.formatOptions = [.withInternetDateTime]
        d.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let raw = try container.decode(String.self)
            if let date = withFraction.date(from: raw) { return date }
            if let date = noFraction.date(from: raw) { return date }
            throw DecodingError.dataCorruptedError(
                in: container,
                debugDescription: "Expected RFC3339 / ISO8601 date string, got \(raw)"
            )
        }
        return d
    }()
}

public enum BambooClientError: Error, CustomStringConvertible {
    case invalidResponse
    case httpStatus(code: Int, body: String)

    public var description: String {
        switch self {
        case .invalidResponse: return "controller returned non-HTTP response"
        case .httpStatus(let code, let body): return "controller status \(code): \(body)"
        }
    }
}
