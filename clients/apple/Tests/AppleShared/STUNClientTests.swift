// SPDX-License-Identifier: Apache-2.0

import XCTest
import Darwin

final class STUNClientTests: XCTestCase {
    /// A UDP socket on loopback that reads nothing and answers nothing.
    private func silentServer() throws -> (fd: Int32, port: UInt16) {
        let fd = socket(AF_INET, SOCK_DGRAM, 0)
        XCTAssertGreaterThanOrEqual(fd, 0)
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = 0
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        XCTAssertEqual(bound, 0)
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        _ = withUnsafeMutablePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(fd, $0, &len) }
        }
        return (fd, UInt16(bigEndian: addr.sin_port))
    }

    /// The heartbeat loop calls discover every 30s. When the reply never
    /// arrives it must still return, or the loop stops for good.
    func testNoReplyThrowsInsteadOfHanging() async throws {
        let (fd, port) = try silentServer()
        defer { close(fd) }

        let returned = expectation(description: "discover returned")
        let task = Task {
            do {
                _ = try await STUNClient.discover(server: "127.0.0.1:\(port)", timeoutSeconds: 0.5)
                XCTFail("a silent server cannot produce an address")
            } catch {
                // Any error is fine; coming back at all is the point.
            }
            returned.fulfill()
        }
        await fulfillment(of: [returned], timeout: 5)
        task.cancel()
    }
}
