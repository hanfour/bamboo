// SPDX-License-Identifier: Apache-2.0

// Entry point for the macOS System Extension binary. App Extensions
// (Contents/PlugIns/*.appex) get an implicit `NSExtensionMain` entry
// point via the NSExtensionPrincipalClass; System Extensions are
// standalone executables and need a real `main`.
//
// `NEProvider.startSystemExtensionMode()` is the canonical
// NetworkExtension entry point — it reads the `NetworkExtension` dict
// from Info.plist (NEMachServiceName + NEProviderClasses), stands up
// the XPC service, and registers the right NEProvider subclass for
// each declared extension point. `dispatchMain()` then parks the
// thread; the runloop is driven by GCD blocks the framework
// schedules.

import Foundation
import NetworkExtension

autoreleasepool {
    NEProvider.startSystemExtensionMode()
}

dispatchMain()
