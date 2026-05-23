// SPDX-License-Identifier: Apache-2.0

// macOS variant of the MagicDNS resolver. The Swift code is identical
// to clients/apple/DNSProxy-iOS/DNSProxyProvider.swift — the
// difference is the *packaging*: on macOS this is a System Extension
// (Contents/Library/SystemExtensions/), installed via
// OSSystemExtensionRequest. The NEDNSProxyProvider subclass and
// per-flow handling are platform-agnostic.

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
    private let store: MagicDNSPeerStore?
    private let id = UUID()

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
                self.flow.closeWriteWithError(error)
                self.release()
                return
            }
            guard let datagrams = datagrams, !datagrams.isEmpty else {
                self.flow.closeReadWithError(nil)
                self.flow.closeWriteWithError(nil)
                self.release()
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
        let peers = store?.peers() ?? [:]
        switch MagicDNSResolver.handle(query: query, peers: peers) {
        case .answered(let response):
            flow.writeDatagrams([response], sentBy: [endpoint]) { error in
                if let error = error {
                    os_log(.error, log: DNSProxyProvider.log,
                           "writeDatagrams (synthesized) failed: %{public}@",
                           String(describing: error))
                }
            }
        case .forwardUpstream:
            forwardUpstream(query: query, replyEndpoint: endpoint)
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

    private func forwardUpstream(query: Data, replyEndpoint: NWHostEndpoint) {
        guard let port = Network.NWEndpoint.Port(replyEndpoint.port) else { return }
        let host = Network.NWEndpoint.Host(replyEndpoint.hostname)
        let conn = NWConnection(host: host, port: port, using: .udp)
        let flow = self.flow

        let timer = DispatchSource.makeTimerSource()
        timer.schedule(deadline: .now() + DNSProxyProvider.upstreamTimeout)
        timer.setEventHandler {
            conn.cancel()
            os_log(.debug, log: DNSProxyProvider.log,
                   "upstream timeout for %{public}@", replyEndpoint.hostname)
        }

        conn.stateUpdateHandler = { state in
            switch state {
            case .ready:
                conn.send(content: query,
                          completion: NWConnection.SendCompletion.contentProcessed { error in
                    if let error = error {
                        os_log(.error, log: DNSProxyProvider.log,
                               "upstream send failed: %{public}@",
                               String(describing: error))
                        timer.cancel()
                        conn.cancel()
                        return
                    }
                    conn.receiveMessage { data, _, _, recvErr in
                        timer.cancel()
                        defer { conn.cancel() }
                        if let recvErr = recvErr {
                            os_log(.error, log: DNSProxyProvider.log,
                                   "upstream receive failed: %{public}@",
                                   String(describing: recvErr))
                            return
                        }
                        guard let data = data else { return }
                        flow.writeDatagrams([data], sentBy: [replyEndpoint]) { writeErr in
                            if let writeErr = writeErr {
                                os_log(.error, log: DNSProxyProvider.log,
                                       "flow write (forwarded) failed: %{public}@",
                                       String(describing: writeErr))
                            }
                        }
                    }
                })
            case .failed(let error):
                timer.cancel()
                os_log(.error, log: DNSProxyProvider.log,
                       "upstream NWConnection failed: %{public}@",
                       String(describing: error))
            case .cancelled:
                timer.cancel()
            default:
                break
            }
        }
        conn.start(queue: DispatchQueue.global(qos: .userInitiated))
        timer.resume()
    }
}
