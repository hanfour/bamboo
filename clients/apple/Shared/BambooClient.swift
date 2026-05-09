// SPDX-License-Identifier: Apache-2.0

import Foundation

/// BambooClient is the minimal REST surface the macOS / iOS app uses
/// to talk to the bamboo controller. The shape of every method matches
/// the controller's JSON wire format under apps/controller/internal/server/.
///
/// Authentication precedence used here:
///
///   - Bearer token (preferred; from completed OIDC sign-in)
///   - X-Tenant-Slug header (dev fallback; controller resolves to the
///     "default" tenant when neither is supplied)
///   - Per-call preAuthKeySecret on register()
public struct BambooClient {
    public let baseURL: URL
    public var bearerToken: String?
    public var tenantSlug: String?

    public init(baseURL: URL, bearerToken: String? = nil, tenantSlug: String? = nil) {
        self.baseURL = baseURL
        self.bearerToken = bearerToken
        self.tenantSlug = tenantSlug
    }

    public struct RegisterRequest: Encodable {
        public var hostname: String
        public var wireguardPublicKey: String
        public var os: String
        public var clientVersion: String
        public var preAuthKeySecret: String?
        public var tenantSlug: String?
        public var endpoints: [String]?
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
    }

    public struct RegisterResponse: Decodable {
        public var self_: PeerJSON
        public var peers: [PeerJSON]
        public var policyRevision: Int64

        enum CodingKeys: String, CodingKey {
            case self_ = "self"
            case peers, policyRevision
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
    }

    public struct HeartbeatResponse: Decodable {
        public var peersChanged: Bool
        public var policyChanged: Bool
        public var currentPolicyRevision: Int64
    }

    public func register(_ req: RegisterRequest) async throws -> RegisterResponse {
        return try await postJSON("/api/v1/peers/register", req)
    }

    public func heartbeat(_ req: HeartbeatRequest) async throws -> HeartbeatResponse {
        return try await postJSON("/api/v1/peers/heartbeat", req)
    }

    public func me() async throws -> MeResponse {
        return try await getJSON("/api/v1/me")
    }

    // MARK: - private

    private func getJSON<T: Decodable>(_ path: String) async throws -> T {
        var req = URLRequest(url: baseURL.appendingPathComponent(path))
        req.httpMethod = "GET"
        applyHeaders(to: &req)
        return try await sendAndDecode(req)
    }

    private func postJSON<Req: Encodable, Resp: Decodable>(_ path: String, _ body: Req) async throws -> Resp {
        var req = URLRequest(url: baseURL.appendingPathComponent(path))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        applyHeaders(to: &req)
        req.httpBody = try JSONEncoder().encode(body)
        return try await sendAndDecode(req)
    }

    private func applyHeaders(to req: inout URLRequest) {
        if let tok = bearerToken, !tok.isEmpty {
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
        return try JSONDecoder().decode(T.self, from: data)
    }
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
