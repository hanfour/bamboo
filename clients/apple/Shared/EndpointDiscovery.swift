// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(Darwin)
import Darwin
#endif

/// Works out the addresses other peers can try to reach this device
/// directly.
///
/// STUN from a throwaway socket reports the public address that socket got.
/// WireGuard's own socket may get a different port. Many home routers keep
/// the source port when they translate, so when the probe socket's public
/// port equals its local port, the same router will most likely map
/// WireGuard's port to itself and `publicIP:wgPort` is dialable. When the
/// port changed, the router is picking ports per flow and no address can be
/// advertised, so the list holds only LAN addresses.
public enum EndpointDiscovery {
    public typealias STUNProbe = @Sendable (_ localPort: UInt16) async throws -> String

    /// Candidates in the order peers should try them: LAN first (cheapest
    /// when it works), then the public address.
    public static func candidates(wgPort: UInt16,
                                  lanAddresses: [String] = lanIPv4Addresses(),
                                  probe: STUNProbe = { try await STUNClient.discover(localPort: $0) }) async -> [String] {
        var out = lanAddresses.map { "\($0):\(wgPort)" }
        if let pub = await publicCandidate(wgPort: wgPort, probe: probe) {
            out.append(pub)
        }
        return out
    }

    static func publicCandidate(wgPort: UInt16, probe: STUNProbe) async -> String? {
        let local = UInt16.random(in: 49152...65000)
        guard let mapped = try? await probe(local),
              let (ip, port) = split(mapped) else { return nil }
        guard port == local else { return nil }
        return "\(ip):\(wgPort)"
    }

    /// True when `candidate` is on the same /24 as one of `lanAddresses`,
    /// which is the only case a private-range candidate can be right.
    public static func isReachableLAN(_ candidate: String, from lanAddresses: [String]) -> Bool {
        guard let (host, _) = split(candidate), isPrivate(host) else { return true }
        let prefix = host.split(separator: ".").prefix(3)
        return lanAddresses.contains { $0.split(separator: ".").prefix(3) == prefix }
    }

    /// Private IPv4 addresses on Wi-Fi and Ethernet. Cellular is left out:
    /// its private addresses belong to the carrier, not to a LAN peers share.
    public static func lanIPv4Addresses() -> [String] {
        var out: [String] = []
        var ifap: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifap) == 0, let first = ifap else { return out }
        defer { freeifaddrs(ifap) }
        var cur: UnsafeMutablePointer<ifaddrs>? = first
        while let p = cur {
            defer { cur = p.pointee.ifa_next }
            let flags = Int32(p.pointee.ifa_flags)
            guard flags & IFF_UP != 0, flags & IFF_LOOPBACK == 0,
                  let sa = p.pointee.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET) else { continue }
            let name = String(cString: p.pointee.ifa_name)
            guard name.hasPrefix("en") else { continue }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            if getnameinfo(sa, socklen_t(sa.pointee.sa_len), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 {
                let ip = String(cString: host)
                if isPrivate(ip), !out.contains(ip) { out.append(ip) }
            }
        }
        return out
    }

    static func isPrivate(_ ip: String) -> Bool {
        let o = ip.split(separator: ".").compactMap { Int($0) }
        guard o.count == 4 else { return false }
        return o[0] == 10 || (o[0] == 172 && (16...31).contains(o[1])) || (o[0] == 192 && o[1] == 168)
    }

    static func split(_ hostPort: String) -> (String, UInt16)? {
        guard let i = hostPort.lastIndex(of: ":"),
              let port = UInt16(hostPort[hostPort.index(after: i)...]) else { return nil }
        return (String(hostPort[..<i]), port)
    }
}
