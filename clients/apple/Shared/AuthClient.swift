// SPDX-License-Identifier: Apache-2.0

import AuthenticationServices
import Foundation
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

/// AuthClient wraps `ASWebAuthenticationSession` so the macOS and iOS
/// apps can drive the controller's OIDC flow without copy-pasting a
/// pre-auth key. The high-level shape:
///
///   1. App constructs `<controllerURL>/auth/google/login?tenant=...&app_callback=bamboo://auth/callback`
///   2. ASWebAuthenticationSession opens the URL in a system-provided
///      Safari surface — user signs in with Google.
///   3. Controller exchanges the code, mints a session JWT, then 302s
///      to `bamboo://auth/callback?token=...&tenant=...&email=...`.
///   4. The OS intercepts the bamboo:// navigation (because we register
///      the scheme + pass it as `callbackURLScheme` here) and hands the
///      URL back to this AuthClient.
///   5. We parse the query params and return them to the caller.
///
/// The session JWT lifecycle (Keychain persistence, Settings UI, etc.)
/// is the caller's responsibility — AuthClient is intentionally just a
/// glue layer over ASWebAuthenticationSession.
@MainActor
public final class AuthClient: NSObject {

    /// SignInResult is the parsed bamboo://auth/callback payload.
    public struct SignInResult {
        public let token: String
        public let tenant: String
        public let email: String
    }

    public override init() {
        super.init()
    }

    /// Provider identifies which controller route to hit. Phase 1 only
    /// implements Google (matching the controller's enum), but the
    /// argument is here so callers don't have to refactor when GitHub
    /// gains a parallel CTA.
    public enum Provider: String {
        case google
        case github
    }

    /// signIn starts the OIDC flow against the controller hosted at
    /// `controllerURL` and resolves with the parsed callback payload
    /// once the user completes the consent screen. Throws AuthError on
    /// cancel / parse failure / underlying ASWebAuthenticationSession
    /// error.
    public func signIn(controllerURL: URL,
                       tenantSlug: String,
                       provider: Provider = .google) async throws -> SignInResult {
        let url = try buildAuthorizationURL(controllerURL: controllerURL,
                                            tenantSlug: tenantSlug,
                                            provider: provider)

        let callbackURL: URL = try await withCheckedThrowingContinuation { continuation in
            // Hold a reference so the session isn't deallocated mid-
            // flight. ASWebAuthenticationSession's completion handler
            // captures `self`, but the session object itself must
            // outlive the user's interaction with Safari.
            var session: ASWebAuthenticationSession?
            session = ASWebAuthenticationSession(
                url: url,
                callbackURLScheme: AuthClient.callbackScheme
            ) { url, error in
                _ = session  // keep alive until completion fires
                if let error = error {
                    if let asError = error as? ASWebAuthenticationSessionError,
                       asError.code == .canceledLogin {
                        continuation.resume(throwing: AuthError.userCancelled)
                    } else {
                        continuation.resume(throwing: AuthError.underlying(error))
                    }
                    return
                }
                guard let url = url else {
                    continuation.resume(throwing: AuthError.emptyCallback)
                    return
                }
                continuation.resume(returning: url)
            }
            // prefersEphemeralWebBrowserSession=true skips the system
            // browser's cookie store — every sign-in starts cold. Good
            // hygiene for a VPN client (no lingering identity between
            // devs / tenants) and lets dev re-test the flow without
            // logging out of Google in Safari.
            session?.prefersEphemeralWebBrowserSession = true
            session?.presentationContextProvider = self
            session?.start()
        }

        return try parseCallback(callbackURL)
    }

    // MARK: - private

    /// callbackScheme MUST match the scheme registered in both apps'
    /// CFBundleURLTypes and the controller's `validateAppCallback`
    /// allow-list. Keeping it as a constant on the class keeps the
    /// three sites linked.
    public static let callbackScheme = "bamboo"

    /// callbackURI is the full string the controller redirects to. Both
    /// halves of the contract live here:
    ///   - controller-side: this exact string passes the
    ///     `validateAppCallback` prefix check.
    ///   - apple-side: this URI must match the scheme registered in
    ///     Info.plist; the host/path components are advisory (the OS
    ///     captures any URL whose scheme matches callbackURLScheme).
    public static let callbackURI = "bamboo://auth/callback"

    private func buildAuthorizationURL(controllerURL: URL,
                                       tenantSlug: String,
                                       provider: Provider) throws -> URL {
        guard var comps = URLComponents(
            url: controllerURL.appendingPathComponent("auth/\(provider.rawValue)/login"),
            resolvingAgainstBaseURL: false
        ) else {
            throw AuthError.invalidControllerURL
        }
        var items = comps.queryItems ?? []
        items.append(URLQueryItem(name: "tenant", value: tenantSlug))
        items.append(URLQueryItem(name: "app_callback", value: AuthClient.callbackURI))
        comps.queryItems = items
        guard let final = comps.url else {
            throw AuthError.invalidControllerURL
        }
        return final
    }

    private func parseCallback(_ url: URL) throws -> SignInResult {
        guard let comps = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            throw AuthError.malformedCallback("not a URL")
        }
        var token: String?
        var tenant: String?
        var email: String?
        for item in comps.queryItems ?? [] {
            switch item.name {
            case "token":  token  = item.value
            case "tenant": tenant = item.value
            case "email":  email  = item.value
            default: break
            }
        }
        guard let token = token, !token.isEmpty else {
            throw AuthError.malformedCallback("token missing")
        }
        // tenant / email default to empty rather than throw — a fork
        // could legitimately omit them, and the JWT is the load-
        // bearing piece. The UI degrades cleanly when email is unknown.
        return SignInResult(token: token,
                            tenant: tenant ?? "",
                            email: email ?? "")
    }
}

extension AuthClient: ASWebAuthenticationPresentationContextProviding {
    public func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        #if canImport(UIKit)
        // Look up the active foreground window. With multi-scene iOS
        // a hardcoded UIApplication.shared.keyWindow can be nil or
        // wrong; iterate through connectedScenes for the active one.
        let scenes = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .filter { $0.activationState == .foregroundActive }
        if let window = scenes.flatMap({ $0.windows }).first(where: { $0.isKeyWindow }) {
            return window
        }
        // Fallback: any window we can find. The OS still presents the
        // session as a sheet, anchored to the app's window hierarchy.
        return scenes.flatMap({ $0.windows }).first ?? ASPresentationAnchor()
        #elseif canImport(AppKit)
        // The menu-bar entry presents its UI from MenuBarExtra; we
        // don't always have a key window. Returning a fresh anchor
        // lets ASWebAuthenticationSession choose its own attachment.
        return NSApplication.shared.keyWindow ?? ASPresentationAnchor()
        #else
        return ASPresentationAnchor()
        #endif
    }
}

public enum AuthError: Error, CustomStringConvertible {
    case invalidControllerURL
    case userCancelled
    case emptyCallback
    case malformedCallback(String)
    case underlying(Error)

    public var description: String {
        switch self {
        case .invalidControllerURL:
            return "controller URL is not a valid base for /auth/* routes"
        case .userCancelled:
            return "sign-in cancelled"
        case .emptyCallback:
            return "auth session returned no callback URL"
        case .malformedCallback(let why):
            return "callback URL malformed: \(why)"
        case .underlying(let err):
            return "auth session: \(err)"
        }
    }
}
