// SPDX-License-Identifier: Apache-2.0

import Foundation
import Network
import NetworkExtension
import os.log

/// DNSProxyProvider is bamboo's MagicDNS resolver running inside a
/// `com.apple.networkextension.dns-proxy` extension. When enabled by
/// the host app via NEDNSProxyManager, the OS routes every DNS
/// query the device makes to this process via `handleNewFlow:`.
///
/// Strategy is forward-by-default:
///   * Parse the question section of the inbound query.
///   * If the name has the `.bamboo` suffix, answer from the App
///     Group-shared peer map (`MagicDNSPeerStore`). Synthesize
///     A/AAAA/NXDOMAIN/NOERROR-empty as appropriate.
///   * Otherwise, forward the raw query to the upstream DNS server
///     the system was going to use (per-datagram endpoint reported
///     by the flow) and pipe the response back. Keeps the per-
///     device DNS experience unchanged for non-bamboo names.
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
        DNSFlowHandler(flow: udp).start()
        return true
    }
}

/// DNSFlowHandler owns the lifetime of a single DNS UDP flow. One
/// handler per flow; the provider hands off and forgets.
///
/// The `NWEndpoint` symbol resolves ambiguously when both `Network`
/// and `NetworkExtension` are imported. The flow's read/write APIs
/// take the NetworkExtension flavor; we let Swift infer it from
/// the closure context rather than annotate explicitly (the
/// explicit annotation hits the "ambiguous type name in module
/// 'NetworkExtension'" path because that module re-exports both).
/// `NWHostEndpoint` is unambiguous and only lives in
/// NetworkExtension, so we use that for the upstream-host
/// extraction.
private final class DNSFlowHandler {

    private let flow: NEAppProxyUDPFlow
    private let store: MagicDNSPeerStore?

    init(flow: NEAppProxyUDPFlow) {
        self.flow = flow
        self.store = MagicDNSPeerStore()
    }

    func start() {
        flow.open(withLocalEndpoint: nil) { [self] error in
            if let error = error {
                os_log(.error, log: DNSProxyProvider.log,
                       "flow open failed: %{public}@",
                       String(describing: error))
                self.flow.closeReadWithError(error)
                self.flow.closeWriteWithError(error)
                return
            }
            self.readLoop()
        }
    }

    private func readLoop() {
        // We use the macOS-13 / iOS-13 parallel-array form
        // ([Data]?, [NWEndpoint]?, Error?). The macOS-15 array-of-
        // tuples form would be tidier but raises the deployment
        // floor — not worth it just to avoid one zip.
        flow.readDatagrams { [self] datagrams, endpoints, error in
            if let error = error {
                self.flow.closeReadWithError(error)
                self.flow.closeWriteWithError(error)
                return
            }
            guard let datagrams = datagrams, !datagrams.isEmpty else {
                self.flow.closeReadWithError(nil)
                self.flow.closeWriteWithError(nil)
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

    /// `endpoint` is the NetworkExtension NWEndpoint — let Swift
    /// pick the type up from the surrounding closure's parameter
    /// list; an explicit annotation runs into the ambiguity.
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

    // Overload bridging the loose `Any`-like endpoint type Swift
    // infers in the readDatagrams closure to the concrete
    // NWHostEndpoint we operate on. Non-host endpoints (Bonjour)
    // are exotic for DNS and dropped silently.
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

        // Single-shot timer protecting against an upstream that
        // accepts the packet but never replies; otherwise the
        // flow leaks an NWConnection until the OS reaps the
        // extension.
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
