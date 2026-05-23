// SPDX-License-Identifier: Apache-2.0

// Entry point for the macOS PacketTunnelProvider System Extension
// binary. The App Extension form (.appex) was retired when the
// tunnel migrated to a System Extension — App Extensions get an
// implicit `NSExtensionMain` from their NSExtensionPrincipalClass,
// but System Extensions are standalone executables that need a
// real `main`. Mirrors DNSProxy-macOS/main.swift.
//
// `NEProvider.startSystemExtensionMode()` reads the
// `NetworkExtension` dict in Info.plist (NEMachServiceName +
// NEProviderClasses) and stands up the XPC service for the
// declared extension point (com.apple.networkextension.packet-tunnel
// here). `dispatchMain()` parks the thread; the runloop is driven
// by GCD blocks the framework schedules.

import Foundation
import NetworkExtension

autoreleasepool {
    NEProvider.startSystemExtensionMode()
}

dispatchMain()
