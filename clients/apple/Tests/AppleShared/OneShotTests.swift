// SPDX-License-Identifier: Apache-2.0

import XCTest

final class OneShotTests: XCTestCase {
    func testFirstResultWinsAndLaterOnesAreIgnored() async throws {
        let value: Int = try await withCheckedThrowingContinuation { cont in
            let shot = OneShot(cont)
            XCTAssertTrue(shot.finish(.success(1)))
            XCTAssertFalse(shot.finish(.success(2)))
            XCTAssertFalse(shot.finish(.failure(AuthError.userCancelled)))
        }
        XCTAssertEqual(value, 1)
    }

    /// A sign-in window that is closed without any callback must still end.
    func testDeadlineEndsAWait() async {
        do {
            let _: Int = try await withCheckedThrowingContinuation { cont in
                let shot = OneShot(cont)
                shot.finish(after: 0.2, with: .failure(AuthError.timedOut))
            }
            XCTFail("expected the deadline to fail the wait")
        } catch {
            guard case AuthError.timedOut = error else { return XCTFail("got \(error)") }
        }
    }

    func testResultBeforeDeadlineKeepsTheResultAndNotTheTimeout() async throws {
        let value: Int = try await withCheckedThrowingContinuation { cont in
            let shot = OneShot(cont)
            shot.finish(after: 0.2, with: .failure(AuthError.timedOut))
            shot.finish(.success(7))
        }
        XCTAssertEqual(value, 7)
    }
}
