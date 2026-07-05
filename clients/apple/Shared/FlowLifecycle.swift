// SPDX-License-Identifier: Apache-2.0

import Foundation

/// FlowLifecycle reconciles a DNS-proxy flow's read-EOF against the
/// async responses still in flight, so the flow's *write* half is torn
/// down exactly once and only after every pending response has been
/// written.
///
/// The bug it fixes: `DNSProxyProvider.readLoop` re-arms `readDatagrams`
/// right after dispatching a query, and a single-query UDP DNS flow then
/// hits read-EOF — which used to `closeWriteWithError` immediately. A
/// MagicDNS answer is written synchronously inside the read callback so
/// it slips in before the close, but the DNS64 path defers its write
/// behind up to two async upstream round-trips, so the synthesised AAAA
/// write landed *after* the write half was already closed and failed
/// with "flow is not connected". (The single-round-trip plain-forward /
/// A path usually beat the close, which is why A resolved but AAAA
/// synthesis silently never did.)
///
/// Usage: `begin()` when dispatching an async response, `end()` in its
/// completion, `markReadClosed()` on read-EOF/error. Each of `end()` and
/// `markReadClosed()` returns `true` exactly once — the moment the flow
/// is both read-closed and idle — signalling the caller to close the
/// write half + release. Pure + lock-guarded so it unit-tests without a
/// real `NEAppProxyFlow`.
final class FlowLifecycle {
    private let lock = NSLock()
    private var inFlight = 0
    private var readClosed = false
    private var closed = false

    /// Register an in-flight async response. Balanced by exactly one `end()`.
    func begin() {
        lock.lock()
        inFlight += 1
        lock.unlock()
    }

    /// Mark an async response complete. Returns `true` iff this drained the
    /// last response after read had closed — the caller should now close
    /// the write half (fires at most once).
    func end() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        inFlight -= 1
        return finishLocked()
    }

    /// Record read-EOF/error. Returns `true` iff nothing is in flight — the
    /// caller should close the write half now (fires at most once).
    func markReadClosed() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        readClosed = true
        return finishLocked()
    }

    /// The write half closes once: read is done AND no responses pending.
    /// The `closed` latch guarantees a single true across all callers.
    private func finishLocked() -> Bool {
        guard !closed, readClosed, inFlight <= 0 else { return false }
        closed = true
        return true
    }
}
