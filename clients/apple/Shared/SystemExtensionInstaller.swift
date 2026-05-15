// SPDX-License-Identifier: Apache-2.0

#if os(macOS)
import Foundation
import OSLog
import SystemExtensions

/// SystemExtensionInstaller wraps `OSSystemExtensionRequest` in an
/// async surface. The activation lifecycle has half a dozen
/// callbacks (replacement decisions, user-approval handoff,
/// completion, failure); funnelling them through a CheckedContinuation
/// gives MagicDNSManager a single `try await installer.activate()`.
///
/// First-time install on a Mac surfaces *two* user-visible prompts:
///
///   1. macOS displays "bamboo wants to install a system extension"
///      and directs the user to System Settings → Privacy & Security
///      to allow it. Until the user clicks Allow there, `submitRequest`
///      sits in `requestNeedsUserApproval` indefinitely.
///   2. Once allowed, the OS asks for an admin password before
///      installing.
///
/// Subsequent app launches are silent — the OS skips both prompts.
/// Version bumps that change the .systemextension's CFBundleVersion
/// re-prompt for password only (no Privacy & Security trip).
///
/// The installer is single-shot: each `activate()` call creates a
/// fresh request. Callers should hold a single instance and serialize
/// invocations.
@MainActor
public final class SystemExtensionInstaller: NSObject {

    private let log = Logger(subsystem: "dev.hanfour.bamboo.app",
                             category: "SystemExtensionInstaller")
    private let bundleIdentifier: String
    private var continuation: CheckedContinuation<Void, Error>?

    public init(bundleIdentifier: String) {
        self.bundleIdentifier = bundleIdentifier
        super.init()
    }

    /// Submit an activation request and resolve when the OS reports
    /// completion (or rejection / failure). On macOS where the user
    /// dismisses the Privacy & Security prompt, this may stay
    /// pending until they revisit Settings.
    public func activate() async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            self.continuation = cont
            let request = OSSystemExtensionRequest.activationRequest(
                forExtensionWithIdentifier: bundleIdentifier,
                queue: .main
            )
            request.delegate = self
            log.log("submitting activation request for \(self.bundleIdentifier, privacy: .public)")
            OSSystemExtensionManager.shared.submitRequest(request)
        }
    }

    private func resume(throwing error: Error) {
        continuation?.resume(throwing: error)
        continuation = nil
    }

    private func resume() {
        continuation?.resume(returning: ())
        continuation = nil
    }
}

extension SystemExtensionInstaller: OSSystemExtensionRequestDelegate {
    public nonisolated func request(
        _ request: OSSystemExtensionRequest,
        actionForReplacingExtension existing: OSSystemExtensionProperties,
        withExtension ext: OSSystemExtensionProperties
    ) -> OSSystemExtensionRequest.ReplacementAction {
        // Always upgrade — the host app and the system extension
        // share a CFBundleVersion (both driven by
        // $(CURRENT_PROJECT_VERSION)), so a newer app implies a
        // newer extension. `.replace` is the right answer; the
        // alternative `.cancel` would pin the user to a stale
        // extension across app updates.
        return .replace
    }

    public nonisolated func requestNeedsUserApproval(
        _ request: OSSystemExtensionRequest
    ) {
        // No await here — this is a notification, not a blocking
        // step. The continuation stays pending; once the user
        // approves in Privacy & Security, the OS fires didFinish.
        Task { @MainActor in
            self.log.log("system extension needs user approval — open System Settings → Privacy & Security")
        }
    }

    public nonisolated func request(
        _ request: OSSystemExtensionRequest,
        didFinishWithResult result: OSSystemExtensionRequest.Result
    ) {
        Task { @MainActor in
            switch result {
            case .completed:
                self.log.log("system extension activated")
                self.resume()
            case .willCompleteAfterReboot:
                // Rare for development installs; treat as success.
                // The user must reboot for the extension to start
                // serving DNS, but the manager-side save can
                // proceed.
                self.log.warning("system extension will complete after reboot")
                self.resume()
            @unknown default:
                self.resume(throwing: SystemExtensionInstallerError.unknownResult)
            }
        }
    }

    public nonisolated func request(
        _ request: OSSystemExtensionRequest,
        didFailWithError error: Error
    ) {
        Task { @MainActor in
            self.log.error("system extension activation failed: \(String(describing: error), privacy: .public)")
            self.resume(throwing: error)
        }
    }
}

public enum SystemExtensionInstallerError: Error, CustomStringConvertible {
    case unknownResult

    public var description: String {
        switch self {
        case .unknownResult:
            return "OSSystemExtensionRequest returned an unrecognized result"
        }
    }
}
#endif
