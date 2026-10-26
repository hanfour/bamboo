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
}
