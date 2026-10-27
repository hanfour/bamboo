// SPDX-License-Identifier: Apache-2.0

import XCTest

final class RelayMeshTests: XCTestCase {
    private func config(relay: RelayConfig?, peers: [BambooPeerConfig] = []) -> BambooTunnelConfig {
        BambooTunnelConfig(privateKey: "k", address: "100.64.0.4/32", wgListenPort: 51820,
                           peers: peers, relay: relay)
    }

    private let peer = BambooPeerConfig(id: "p1", publicKey: "AAAA", allowedIPs: ["100.64.0.1/32"],
                                        endpoint: "1.2.3.4:51820", persistentKeepalive: 25)

    func testRelayFieldSurvivesJSONRoundTrip() throws {
        let c = config(relay: RelayConfig(url: "wss://relay.example/relay", selfPublicKey: "PUB"))
        let back = try JSONDecoder().decode(BambooTunnelConfig.self, from: JSONEncoder().encode(c))
        XCTAssertEqual(back, c)
        XCTAssertEqual(back.relay?.url, "wss://relay.example/relay")
    }

    func testConfigWrittenBeforeRelayFieldStillDecodes() throws {
        let old = #"{"privateKey":"k","address":"100.64.0.4/32","dnsServers":[],"wgListenPort":0,"peers":[]}"#
        let c = try JSONDecoder().decode(BambooTunnelConfig.self, from: Data(old.utf8))
        XCTAssertNil(c.relay)
    }

    func testReplacingPeersKeepsEverythingElse() {
        let c = config(relay: RelayConfig(url: "wss://r/relay", selfPublicKey: "PUB"))
        let swapped = c.replacingPeers([peer])
        XCTAssertEqual(swapped.peers, [peer])
        XCTAssertEqual(swapped.relay, c.relay)
        XCTAssertEqual(swapped.wgListenPort, 51820)
        XCTAssertEqual(swapped.address, c.address)
    }

    func testNoRelayConfigLeavesPeersUntouched() async {
        let mesh = RelayMesh()
        let c = config(relay: nil, peers: [peer])
        let out = await mesh.apply(c, auth: nil)
        XCTAssertEqual(out, c)
    }

    func testRelayWithoutCredentialsFallsBackToDirect() async {
        let mesh = RelayMesh()
        let c = config(relay: RelayConfig(url: "wss://r/relay", selfPublicKey: "PUB"), peers: [peer])
        let out = await mesh.apply(c, auth: nil)
        XCTAssertEqual(out.peers, [peer], "no session token, so peers keep their direct endpoint")
    }

    func testUnreachableRelayFallsBackToDirect() async {
        let mesh = RelayMesh()
        let auth = PresenceConfig(controllerURL: "http://127.0.0.1:9", tenantSlug: "", peerID: "self", sessionToken: "t")
        let pub = Data(repeating: 7, count: 32).base64EncodedString()
        let c = config(relay: RelayConfig(url: "wss://127.0.0.1:9/relay", selfPublicKey: pub), peers: [peer])
        let out = await mesh.apply(c, auth: auth)
        XCTAssertEqual(out.peers, [peer], "token mint fails, so peers stay direct")
        await mesh.stop()
    }

    func testNoRelayMeansNothingToReport() async {
        let mesh = RelayMesh()
        _ = await mesh.apply(config(relay: nil, peers: [peer]), auth: nil)
        let path = await mesh.connectionPath()
        XCTAssertNil(path)
    }

    func testHeartbeatCarriesConnectionPathOnlyWhenKnown() throws {
        func json(_ path: String?) throws -> [String: Any] {
            let req = BambooClient.HeartbeatRequest(peerId: "p", knownPolicyRevision: 1, connectionPath: path)
            return try JSONSerialization.jsonObject(with: JSONEncoder().encode(req)) as! [String: Any]
        }
        XCTAssertEqual(try json("direct")["connectionPath"] as? String, "direct")
        XCTAssertNil(try json(nil)["connectionPath"], "unknown must not overwrite the controller's value")
    }

    func testBandwidthStatsFromOlderExtensionStillDecodes() throws {
        let old = #"{"bytesSent":5,"bytesReceived":7}"#
        let bw = try JSONDecoder().decode(TunnelIPC.BandwidthStats.self, from: Data(old.utf8))
        XCTAssertEqual(bw.bytesSent, 5)
        XCTAssertNil(bw.connectionPath)
        let new = TunnelIPC.BandwidthStats(bytesSent: 1, bytesReceived: 2, connectionPath: "relay")
        let back = try JSONDecoder().decode(TunnelIPC.BandwidthStats.self, from: JSONEncoder().encode(new))
        XCTAssertEqual(back.connectionPath, "relay")
    }
}
