// SPDX-License-Identifier: Apache-2.0

// macOS variant of the MagicDNS resolver. Mostly parallel to
// clients/apple/DNSProxy-iOS/DNSProxyProvider.swift — the
// difference is the *packaging*: on macOS this is a System Extension
// (Contents/Library/SystemExtensions/), installed via
// OSSystemExtensionRequest. The NEDNSProxyProvider subclass and
// per-flow handling are platform-agnostic.
//
// macOS-specific behavior here: explicit DNSFlowHandler retention
// (process-wide Set) so the handler isn't GC'd before flow.open's
// completion fires. iOS hasn't shown the same lifetime concern in
// practice — App Extensions run in the host app's address space and
// the closure retain seems sufficient — so the iOS sibling kept the
// simpler inline-instantiate pattern.

import Foundation
import Network
import NetworkExtension
import os.log

final class DNSProxyProvider: NEDNSProxyProvider {

    static let log = OSLog(subsystem: "dev.hanfour.bamboo.dnsproxy",
                           category: "DNSProxyProvider")

    static let upstreamTimeout: TimeInterval = 2.0

    override func startProxy(options: [String: Any]? = nil,
                             completionHandler: @escaping (Error?) -> Void) {
        os_log(.info, log: Self.log, "startProxy")
        completionHandler(nil)
    }

    override func stopProxy(with reason: NEProviderStopReason,
                            completionHandler: @escaping () -> Void) {
        os_log(.info, log: Self.log, "stopProxy reason=%{public}d", reason.rawValue)
        completionHandler()
    }

    override func handleNewFlow(_ flow: NEAppProxyFlow) -> Bool {
        guard let udp = flow as? NEAppProxyUDPFlow else {
            return false
        }
        // Retain the handler explicitly. The closures captured for
        // flow.open / readDatagrams capture self, so in normal
        // operation the handler stays alive while the flow has
        // outstanding I/O. Holding an explicit reference here
        // hardens against any future framework change that might
        // release the closures synchronously before the first
        // read completes; the handler self-releases via `release()`
        // when the flow closes.
        let handler = DNSFlowHandler(flow: udp)
        Self.handlersLock.lock()
        Self.handlers.insert(handler)
        Self.handlersLock.unlock()
        handler.start()
        return true
    }

    fileprivate static let handlersLock = NSLock()
    fileprivate static var handlers: Set<DNSFlowHandler> = []
}

fileprivate final class DNSFlowHandler: Hashable {

    private let flow: NEAppProxyUDPFlow
    private let store: MagicDNSPeerStore
    private let id = UUID()
    // Reconciles read-EOF against still-in-flight async responses so the
    // write half is torn down exactly once, only after every pending
    // response has been written. Without it, a single-query flow's
    // read-EOF closed the write half before the deferred DNS64 synthesis
    // could write — the synthesised AAAA failed with "flow is not
    // connected" while the synchronous MagicDNS / single-round-trip A
    // paths slipped in under the close.
    private let lifecycle = FlowLifecycle()

    init(flow: NEAppProxyUDPFlow) {
        self.flow = flow
        self.store = MagicDNSPeerStore()
    }

    static func == (lhs: DNSFlowHandler, rhs: DNSFlowHandler) -> Bool { lhs.id == rhs.id }
    func hash(into hasher: inout Hasher) { hasher.combine(id) }

    private func release() {
        DNSProxyProvider.handlersLock.lock()
        DNSProxyProvider.handlers.remove(self)
        DNSProxyProvider.handlersLock.unlock()
    }

    /// finishWriteClose tears down the write half and releases the
    /// handler. Invoked exactly once — by whichever of read-EOF or the
    /// last draining response wins the FlowLifecycle latch — so a pending
    /// synthesised write is never cut off mid-flight.
    private func finishWriteClose(_ error: Error?) {
        flow.closeWriteWithError(error)
        release()
    }

    func start() {
        flow.open(withLocalEndpoint: nil) { [self] error in
            if let error = error {
                os_log(.error, log: DNSProxyProvider.log,
                       "flow open failed: %{public}@",
                       String(describing: error))
                self.flow.closeReadWithError(error)
                self.flow.closeWriteWithError(error)
                self.release()
                return
            }
            self.readLoop()
        }
    }

    private func readLoop() {
        flow.readDatagrams { [self] datagrams, endpoints, error in
            if let error = error {
                self.flow.closeReadWithError(error)
                // Defer the write-half teardown until in-flight responses
                // drain — closing it now would drop a pending synthesised
                // AAAA (the write-after-close race).
                if self.lifecycle.markReadClosed() { self.finishWriteClose(error) }
                return
            }
            guard let datagrams = datagrams, !datagrams.isEmpty else {
                self.flow.closeReadWithError(nil)
                if self.lifecycle.markReadClosed() { self.finishWriteClose(nil) }
                return
            }
            let endpoints = endpoints ?? []
            for (i, query) in datagrams.enumerated() {
                guard i < endpoints.count else { break }
                self.handleOne(query: query, endpoint: endpoints[i])
            }
            self.readLoop()
        }
    }

    private func handleOne(query: Data, endpoint: NWHostEndpoint) {
        let peers = store.peers()
        switch MagicDNSResolver.handle(query: query, peers: peers) {
        case .answered(let response):
            lifecycle.begin()
            flow.writeDatagrams([response], sentBy: [endpoint]) { [self] error in
                if let error = error {
                    os_log(.error, log: DNSProxyProvider.log,
                           "writeDatagrams (synthesized) failed: %{public}@",
                           String(describing: error))
                }
                if self.lifecycle.end() { self.finishWriteClose(nil) }
            }
        case .forwardUpstream:
            lifecycle.begin()
            maybeDNS64(query: query, endpoint: endpoint) { [self] in
                if self.lifecycle.end() { self.finishWriteClose(nil) }
            }
        case .malformed:
            os_log(.debug, log: DNSProxyProvider.log,
                   "dropping malformed query (%{public}d bytes)", query.count)
        }
    }

    private func handleOne(query: Data, endpoint: Any) {
        guard let host = endpoint as? NWHostEndpoint else {
            os_log(.debug, log: DNSProxyProvider.log,
                   "non-host source endpoint; dropping query")
            return
        }
        handleOne(query: query, endpoint: host)
    }

    /// writeToFlow sends one datagram back to the asking app, then calls
    /// `done` from the write completion — so the FlowLifecycle op stays
    /// in flight until the write has actually landed (not merely been
    /// enqueued), which is what keeps the write half open long enough.
    private func writeToFlow(_ data: Data, _ endpoint: NWHostEndpoint,
                             done: @escaping () -> Void) {
        flow.writeDatagrams([data], sentBy: [endpoint]) { error in
            if let error = error {
                os_log(.error, log: DNSProxyProvider.log,
                       "flow write failed: %{public}@", String(describing: error))
            }
            done()
        }
    }

    /// maybeDNS64 decides whether a to-be-forwarded query is a DNS64
    /// candidate. If DNS64 is off, or the query isn't a synthesizable
    /// external AAAA, it forwards verbatim. Otherwise it runs the RFC 6147
    /// 2-stage path: AAAA upstream → (NODATA?) → A upstream → synthesise.
    /// `onComplete` fires exactly once at every terminal path (after the
    /// reply write lands, or immediately when nothing is written).
    private func maybeDNS64(query: Data, endpoint: NWHostEndpoint,
                            onComplete: @escaping () -> Void) {
        let cfg = store.nat64Config()
        guard cfg.dns64Enabled,
              let msg = DNSMessage.parse(query),
              NAT64Synth.shouldSynthesize(query: msg, dns64Enabled: true,
                                          zone: MagicDNSResolver.zone),
              let prefix = NAT64Synth.prefixBytes(cfg.nat64Prefix) else {
            forwardUpstream(query: query, replyEndpoint: endpoint, onComplete: onComplete)
            return
        }
        // Stage 1: forward the AAAA query.
        upstreamQuery(query, to: endpoint) { [self] aaaaResp in
            guard let aaaaResp = aaaaResp else { onComplete(); return } // timeout/fail → client retries
            // A real AAAA (or an error rcode) wins — relay verbatim (RFC 6147).
            if !DNSMessage.isDNS64Candidate(aaaaResp) {
                self.writeToFlow(aaaaResp, endpoint, done: onComplete)
                return
            }
            // Stage 2: query the same name's A record, then synthesise.
            guard let aQuery = DNSMessage.aQueryFromAAAA(query) else {
                self.writeToFlow(aaaaResp, endpoint, done: onComplete) // can't build A query → relay NODATA
                return
            }
            self.upstreamQuery(aQuery, to: endpoint) { [self] aResp in
                guard let aResp = aResp,
                      let synth = DNSMessage.synthesizeResponse(
                          aaaaQuery: msg, aResponse: aResp, prefix: prefix) else {
                    // Distinguish "all A special-use" (NAT64 §5.1.4 exclusion)
                    // from "no A / upstream fail" so an operator can diagnose a
                    // name that resolves but never gets a synthesised AAAA.
                    if let aResp = aResp, let v4s = DNSMessage.aRecords(aResp), !v4s.isEmpty {
                        os_log(.debug, log: DNSProxyProvider.log,
                               "dns64 skip: all %{public}d A special-use for %{public}@",
                               v4s.count, msg.questions.first?.name ?? "?")
                    }
                    self.writeToFlow(aaaaResp, endpoint, done: onComplete) // no A / fail / all special-use → relay NODATA
                    return
                }
                self.writeToFlow(synth, endpoint, done: onComplete)
            }
        }
    }

    /// forwardUpstream relays a query upstream and writes the reply back to
    /// the flow verbatim (the non-DNS64 path). `onComplete` fires once.
    private func forwardUpstream(query: Data, replyEndpoint: NWHostEndpoint,
                                 onComplete: @escaping () -> Void) {
        upstreamQuery(query, to: replyEndpoint) { [self] data in
            if let data = data {
                self.writeToFlow(data, replyEndpoint, done: onComplete)
            } else {
                onComplete()
            }
        }
    }

    /// upstreamQuery sends one DNS query to `replyEndpoint` over UDP and
    /// hands the reply bytes to `completion` (nil on timeout / send / receive
    /// / connection failure). The completion fires EXACTLY once — a lock
    /// guards the timeout-vs-receive race.
    private func upstreamQuery(_ query: Data, to replyEndpoint: NWHostEndpoint,
                               completion: @escaping (Data?) -> Void) {
        guard let port = Network.NWEndpoint.Port(replyEndpoint.port) else {
            completion(nil)
            return
        }
        let host = Network.NWEndpoint.Host(replyEndpoint.hostname)
        let conn = NWConnection(host: host, port: port, using: .udp)

        let onceLock = NSLock()
        var done = false
        func finish(_ data: Data?) {
            onceLock.lock()
            let already = done
            done = true
            onceLock.unlock()
            if already { return }
            completion(data)
        }

        let timer = DispatchSource.makeTimerSource()
        timer.schedule(deadline: .now() + DNSProxyProvider.upstreamTimeout)
        timer.setEventHandler {
            conn.cancel()
            os_log(.debug, log: DNSProxyProvider.log,
                   "upstream timeout for %{public}@", replyEndpoint.hostname)
            finish(nil)
        }

        conn.stateUpdateHandler = { state in
            switch state {
            case .ready:
                conn.send(content: query,
                          completion: NWConnection.SendCompletion.contentProcessed { error in
                    if let error = error {
                        os_log(.error, log: DNSProxyProvider.log,
                               "upstream send failed: %{public}@", String(describing: error))
                        timer.cancel()
                        conn.cancel()
                        finish(nil)
                        return
                    }
                    conn.receiveMessage { data, _, _, recvErr in
                        timer.cancel()
                        defer { conn.cancel() }
                        if let recvErr = recvErr {
                            os_log(.error, log: DNSProxyProvider.log,
                                   "upstream receive failed: %{public}@", String(describing: recvErr))
                            finish(nil)
                            return
                        }
                        finish(data)
                    }
                })
            case .failed(let error):
                timer.cancel()
                os_log(.error, log: DNSProxyProvider.log,
                       "upstream NWConnection failed: %{public}@", String(describing: error))
                finish(nil)
            case .cancelled:
                timer.cancel()
                finish(nil)
            default:
                break
            }
        }
        conn.start(queue: DispatchQueue.global(qos: .userInitiated))
        timer.resume()
    }
}
