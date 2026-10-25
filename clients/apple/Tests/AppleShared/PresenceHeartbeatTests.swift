// SPDX-License-Identifier: Apache-2.0

import XCTest

final class PresenceHeartbeatTests: XCTestCase {
    private func makeSession(_ handler: @escaping (URLRequest) -> (Int, Data)) -> URLSession {
        StubURLProtocol.handler = handler
        let cfg = URLSessionConfiguration.ephemeral
        cfg.protocolClasses = [StubURLProtocol.self]
        return URLSession(configuration: cfg)
    }

    private let cfg = PresenceConfig(controllerURL: "https://bamboo.example.com", tenantSlug: "default",
                                     peerID: "peer-1", sessionToken: "tok-1")

    func testBeatPostsPeerIDWithBearerAndTenant() async throws {
        var seen: URLRequest?
        var seenBody: [String: Any]?
        let session = makeSession { req in
            seen = req
            if let stream = req.httpBodyStream {
                stream.open()
                var buf = [UInt8](repeating: 0, count: 4096)
                let n = stream.read(&buf, maxLength: buf.count)
                seenBody = try? JSONSerialization.jsonObject(with: Data(buf[0..<max(n, 0)])) as? [String: Any]
                stream.close()
            }
            return (200, Data("{\"peersChanged\":false,\"policyChanged\":false,\"currentPolicyRevision\":1}".utf8))
        }
        let hb = PresenceHeartbeat(session: session, interval: 3600)
        await hb.apply(cfg)
        try await Task.sleep(nanoseconds: 300_000_000)
        await hb.stop()

        let req = try XCTUnwrap(seen)
        XCTAssertEqual(req.url?.absoluteString, "https://bamboo.example.com/api/v1/peers/heartbeat")
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer tok-1")
        XCTAssertEqual(req.value(forHTTPHeaderField: "X-Tenant-Slug"), "default")
        XCTAssertEqual(seenBody?["peerId"] as? String, "peer-1")
        // No endpoints or bandwidth: the extension must not change anything but last_seen.
        XCTAssertNil(seenBody?["endpoints"])
        XCTAssertNil(seenBody?["bytesSent"])
    }

    func testRenewedTokenIsUsedOnTheNextBeat() async throws {
        var tokens: [String] = []
        let session = makeSession { req in
            tokens.append(req.value(forHTTPHeaderField: "Authorization") ?? "")
            let body = tokens.count == 1
                ? "{\"peersChanged\":false,\"policyChanged\":false,\"currentPolicyRevision\":1,\"peerSessionToken\":\"tok-2\",\"peerSessionExpiresAt\":9999999999}"
                : "{\"peersChanged\":false,\"policyChanged\":false,\"currentPolicyRevision\":1}"
            return (200, Data(body.utf8))
        }
        let hb = PresenceHeartbeat(session: session, interval: 3600)
        await hb.apply(cfg)
        // apply() sends the first beat itself; wait for it so the manual
        // beat below runs after the renewal was stored. In production only
        // the loop calls beat(), one at a time.
        try await Task.sleep(nanoseconds: 400_000_000)
        _ = await hb.beat()
        await hb.stop()
        XCTAssertEqual(tokens, ["Bearer tok-1", "Bearer tok-2"])
    }

    func testRejectedBeatReturnsFalseAndDoesNotCrash() async {
        let session = makeSession { _ in (401, Data("{\"error\":\"expired\"}".utf8)) }
        let hb = PresenceHeartbeat(session: session, interval: 3600)
        await hb.apply(cfg)
        let ok = await hb.beat()
        await hb.stop()
        XCTAssertFalse(ok)
    }

    func testRenewedTokenParsing() {
        XCTAssertEqual(PresenceHeartbeat.renewedToken(in: Data("{\"peerSessionToken\":\"x\"}".utf8)), "x")
        XCTAssertNil(PresenceHeartbeat.renewedToken(in: Data("{\"peerSessionToken\":\"\"}".utf8)))
        XCTAssertNil(PresenceHeartbeat.renewedToken(in: Data("{}".utf8)))
        XCTAssertNil(PresenceHeartbeat.renewedToken(in: Data("not json".utf8)))
    }

    func testOlderConfigWithoutPresenceStillDecodes() throws {
        // A config written before this field existed must still load.
        let json = "{\"controllerURL\":\"https://x\",\"tenantSlug\":\"d\",\"peerID\":\"p\",\"sessionToken\":\"t\"}"
        let decoded = try JSONDecoder().decode(PresenceConfig.self, from: Data(json.utf8))
        XCTAssertEqual(decoded.peerID, "p")
    }

    func testStopThenBeatDoesNothing() async {
        var calls = 0
        let session = makeSession { _ in calls += 1; return (200, Data("{}".utf8)) }
        let hb = PresenceHeartbeat(session: session, interval: 3600)
        await hb.apply(cfg)
        await hb.stop()
        let ok = await hb.beat()
        XCTAssertFalse(ok)
        XCTAssertLessThanOrEqual(calls, 1) // at most the one the loop sent before stop
    }
}

final class StubURLProtocol: URLProtocol {
    nonisolated(unsafe) static var handler: ((URLRequest) -> (Int, Data))?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let handler = StubURLProtocol.handler else { return }
        let (status, data) = handler(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
