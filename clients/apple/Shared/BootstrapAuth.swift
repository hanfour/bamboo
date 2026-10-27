// SPDX-License-Identifier: Apache-2.0

import Foundation

/// The sign-in token lasts 24 hours and is never renewed, while the
/// peer-session token is renewed all day by the running tunnel. Connecting
/// after a day therefore failed with 401 even though the device was still
/// known to the controller. BootstrapAuth tries the sign-in token first and,
/// on 401, retries once with the peer-session token.
public enum BootstrapAuth {
    public struct SessionExpired: Swift.Error, CustomStringConvertible {
        public init() {}
        public var description: String {
            "Your sign-in has expired. Tap Sign out, then sign in again."
        }
    }

    public static func isUnauthorized(_ error: Swift.Error) -> Bool {
        if case BambooClientError.httpStatus(let code, _) = error { return code == 401 }
        return false
    }

    /// Runs `attempt` with the sign-in token. On 401 it asks for a
    /// peer-session token and runs `attempt` once more with it. Throws
    /// `SessionExpired` when there is no such token or it is refused too.
    public static func run<T>(userToken: String?,
                              peerToken: () async -> String?,
                              attempt: (String?) async throws -> T) async throws -> T {
        do {
            return try await attempt(userToken)
        } catch where isUnauthorized(error) {
            guard let fallback = await peerToken(), !fallback.isEmpty, fallback != userToken else {
                throw SessionExpired()
            }
            do {
                return try await attempt(fallback)
            } catch where isUnauthorized(error) {
                throw SessionExpired()
            }
        }
    }
}
