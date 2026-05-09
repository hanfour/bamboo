// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Errors emitted by the bamboo PacketTunnelProvider implementations
/// on either platform. Kept in Shared/ so both extension targets and
/// the host apps can reason about them.
public enum BambooTunnelError: Error {
    case missingConfiguration
}
