// SPDX-License-Identifier: Apache-2.0

import XCTest

final class BootstrapAuthTests: XCTestCase {
    private let unauthorized = BambooClientError.httpStatus(code: 401, body: "{}")

    func testSignInTokenIsUsedFirstAndPeerTokenNotAsked() async throws {
        var asked = false
        let got = try await BootstrapAuth.run(userToken: "user", peerToken: { asked = true; return "peer" },
                                              attempt: { tok in tok ?? "" })
        XCTAssertEqual(got, "user")
        XCTAssertFalse(asked)
    }

    func testExpiredSignInFallsBackToPeerToken() async throws {
        var seen: [String?] = []
        let got = try await BootstrapAuth.run(userToken: "old", peerToken: { "peer" }, attempt: { tok -> String in
            seen.append(tok)
            if tok == "old" { throw self.unauthorized }
            return "ok"
        })
        XCTAssertEqual(got, "ok")
        XCTAssertEqual(seen, ["old", "peer"])
    }

    func testNoPeerTokenMeansSessionExpired() async {
        do {
            _ = try await BootstrapAuth.run(userToken: "old", peerToken: { nil }, attempt: { _ -> Int in throw self.unauthorized })
            XCTFail("expected SessionExpired")
        } catch {
            XCTAssertTrue(error is BootstrapAuth.SessionExpired)
        }
    }

    func testRefusedPeerTokenMeansSessionExpired() async {
        var calls = 0
        do {
            _ = try await BootstrapAuth.run(userToken: "old", peerToken: { "peer" }, attempt: { _ -> Int in
                calls += 1
                throw self.unauthorized
            })
            XCTFail("expected SessionExpired")
        } catch {
            XCTAssertTrue(error is BootstrapAuth.SessionExpired)
            XCTAssertEqual(calls, 2, "one retry, no loop")
        }
    }

    func testOtherErrorsPassThroughUntouched() async {
        var asked = false
        do {
            _ = try await BootstrapAuth.run(userToken: "t", peerToken: { asked = true; return "peer" }, attempt: { _ -> Int in
                throw BambooClientError.httpStatus(code: 500, body: "boom")
            })
            XCTFail("expected the 500")
        } catch BambooClientError.httpStatus(let code, _) {
            XCTAssertEqual(code, 500)
            XCTAssertFalse(asked)
        } catch {
            XCTFail("unexpected \(error)")
        }
    }

    func testSessionTokenSurvivesIPCRoundTrip() throws {
        let resp = TunnelIPC.Response(ok: true, sessionToken: "tok")
        let back = try TunnelIPC.decodeResponse(TunnelIPC.encode(resp))
        XCTAssertEqual(back.sessionToken, "tok")
        let old = #"{"ok":true}"#
        XCTAssertNil(try TunnelIPC.decodeResponse(Data(old.utf8)).sessionToken)
    }
}
