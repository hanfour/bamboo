// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Resumes a continuation exactly once, from whichever of several sources
/// gets there first (a callback, a user cancel, a deadline). Resuming a
/// checked continuation twice traps, and never resuming it hangs the caller
/// for good, so every path goes through here.
public final class OneShot<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, Error>?

    public init(_ continuation: CheckedContinuation<T, Error>) {
        self.continuation = continuation
    }

    /// Returns true when this call was the one that resumed the continuation.
    @discardableResult
    public func finish(_ result: Result<T, Error>) -> Bool {
        lock.lock()
        let cont = continuation
        continuation = nil
        lock.unlock()
        guard let cont else { return false }
        cont.resume(with: result)
        return true
    }

    /// Ends the wait with `result` after `seconds` if nothing finished it first.
    public func finish(after seconds: TimeInterval, with result: Result<T, Error>) {
        DispatchQueue.global().asyncAfter(deadline: .now() + seconds) { [self] in
            finish(result)
        }
    }
}
