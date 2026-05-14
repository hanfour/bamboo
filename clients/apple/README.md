# clients/apple

bamboo native clients for macOS and iOS. SwiftUI app on each platform
talks to a separate `PacketTunnelProvider` NetworkExtension target that
runs the actual WireGuard tunnel.

**License:** Apache 2.0 — see [LICENSE-APACHE](../../LICENSE-APACHE).

## Status

End-to-end working on both platforms (modulo signing — see prerequisites
below). The Swift sources here compile into:

- macOS menu-bar icon + popover with Connect / Disconnect / Settings
- iOS full-screen layout with Connect / Disconnect / Settings sheet
- Settings: controller URL, tenant slug, optional pre-auth key, hostname
- `ConnectionViewModel` is wired: on connect it generates (or loads)
  a Curve25519 keypair from Keychain, calls `/api/v1/peers/register`,
  hands the response to `TunnelManager.startTunnel`
- `TunnelManager` owns the single `NETunnelProviderManager` for the
  bamboo profile, writes a `BambooTunnelConfig` JSON blob into
  `providerConfiguration`, and starts the system tunnel
- `PacketTunnelProvider-{macOS,iOS}` decode that config and bring up a
  WireGuardKit-backed tunnel via `WireGuardAdapter`

What's *not* there yet: peer endpoint discovery (no STUN / relay
surface on the controller yet), so even with two peers registered the
tunnels can't directly reach each other until that lands. Single-peer
test (just bringing up your own tunnel address) works end-to-end.

Theme 4 progress is tracked in
[ADR 0012](../../docs/adr/0012-phase-2-transition.md).

## Repository layout

```
clients/apple/
  project.yml                                    XcodeGen project specification
  Shared/
    ConnectionViewModel.swift                    cross-platform view model
    BambooClient.swift                           REST client for /api/v1/*
    STUNClient.swift                             RFC 5389 binding-request client
    HeartbeatLoop.swift                          periodic POST /api/v1/peers/heartbeat
    PeerWatcher.swift                            SSE consumer for /api/v1/peers/watch
    KeychainStore.swift                          Keychain wrapper (private key, session token)
    TunnelManager.swift                          NETunnelProviderManager driver
    TunnelConfigurationBuilder.swift             JSON config -> WireGuardKit TunnelConfiguration
    BambooTunnelError.swift                      shared error type
    TunnelIPC.swift                              app <-> extension message envelope
    RelayClient.swift                            relay WSS client + per-peer UDP proxy
  BambooApp-macOS/                               macOS menu-bar app
    BambooApp.swift
    ContentView.swift
    Info.plist
    BambooApp.entitlements
  BambooApp-iOS/                                 iOS full-screen app
    BambooApp_iOS.swift
    ContentView.swift
    Info.plist
    BambooApp_iOS.entitlements
  PacketTunnelProvider-macOS/                    macOS NEPacketTunnelProvider stub
    PacketTunnelProvider.swift
    Info.plist
    PacketTunnelProvider.entitlements
  PacketTunnelProvider-iOS/                      iOS NEPacketTunnelProvider stub
    PacketTunnelProvider_iOS.swift
    Info.plist
    PacketTunnelProvider_iOS.entitlements
```

We deliberately do **not** commit `bamboo.xcodeproj`. Xcode rewrites it
on every settings change, which makes diff review and merge resolution
unproductive. We use [XcodeGen](https://github.com/yonaskolb/XcodeGen)
as the source of truth.

## Toolchain prerequisites

One-time setup per developer Mac:

```bash
brew install xcodegen   # generates bamboo.xcodeproj from project.yml
brew install go         # cgo cross-compiles libwg-go.a for WireGuardKit
```

`go` is required because the WireGuardKit SPM package's `WireGuardKitGo`
target ships only Go sources + a Makefile — no prebuilt `libwg-go.a`.
A Run Script build phase invokes the Makefile before linking the app
or extension (see `scripts/build-wireguardkitgo.sh`). Make's dependency
tracking keeps incremental builds ~0s; clean builds add ~30s for the
cgo cross-compile.

### Known limitation: iOS Simulator

The upstream WireGuard Makefile + Go runtime patches only target
`macosx` and `iphoneos` (device). `iphonesimulator` is unsupported —
the resulting `libwg-go.a` references device-only mach exception
handler symbols. Develop on macOS or a physical iOS device; iOS
Simulator builds will fail at link with `_darwin_arm_init_*`
undefined-symbol errors.

## Apple-side prerequisites (one-off)

These are not automatable from a script — they require an Apple
Developer Program account ($99/year) and a real human in the developer
portal.

1. Enroll in Apple Developer Program (or use an existing team).
2. In `developer.apple.com`, request the **Network Extension** capability
   for your team. Apple reviews this manually; allow 1–3 business days.
3. Create the App IDs (`<prefix>.app` and `<prefix>.app.tunnel`) for both
   macOS and iOS, plus an App Group your apps and tunnel extensions
   share.
4. Generate signing certificates / enable Automatic Signing in Xcode.

## Required setup

- Sign in with your Apple ID under `Xcode → Settings → Accounts`. The
  team is auto-selected via `CODE_SIGN_STYLE=Automatic`; you don't need
  to set any environment variable.
- Bundle ID prefix is hardcoded to `dev.hanfour.bamboo` in
  `project.yml`. Forks: search + replace.

## Local build

```bash
brew install xcodegen
cd clients/apple
xcodegen generate                                 # creates bamboo.xcodeproj
open bamboo.xcodeproj                             # build + run from Xcode
```

Pick the `BambooApp-macOS` scheme to run on your Mac, or `BambooApp-iOS`
on a connected iPhone / Simulator.

To bring up the tunnel for the first time you must accept the system
prompt that allows bamboo to add a VPN configuration. On macOS this
lives in `System Settings → VPN`; on iOS the system shows it as a
modal at first connect.

## Why a separate PacketTunnelProvider target?

Both macOS and iOS only allow the kernel to hand packets to a process
that:

- Holds the `com.apple.developer.networking.networkextension` entitlement
- Is registered as a `NEPacketTunnelProvider`
- Runs in its own bundled extension (sandboxed XPC service)

The user-facing app cannot satisfy these constraints because sandboxing
rules forbid raw packet access from regular UI apps. The extension is
short-lived and does only the tunnelling; UI lives in the host app and
talks to the extension through `NEVPNConnection`.

## What's wired vs deferred

| Capability | macOS | iOS |
| --- | --- | --- |
| App shell, Connect / Disconnect / Settings UI | ✅ Phase 2 DDDD | ✅ Phase 2 DDDD |
| App ↔ Extension via `NETunnelProviderManager` | ✅ Phase 2 DDDD | ✅ Phase 2 DDDD |
| Controller registration via REST bridge | ✅ Phase 2 BBBB | ✅ Phase 2 BBBB |
| WireGuard tunnel via WireGuardKit | ✅ Phase 2 CCCC | ✅ Phase 2 CCCC |
| Keychain-backed WG private key persistence | ✅ Phase 2 DDDD | ✅ Phase 2 DDDD |
| OIDC web flow (ASWebAuthenticationSession) | ⏸ Phase 2 follow-up | ⏸ Phase 2 follow-up |
| STUN endpoint discovery (`STUNClient`) | ✅ Phase 2 HHHH | ✅ Phase 2 HHHH |
| Heartbeat + watch loops (peer roaming) | ✅ Phase 2 IIII | ✅ Phase 2 IIII |
| Live tunnel reconfig via IPC (no blip) | ✅ Phase 2 KKKK | ✅ Phase 2 KKKK |
| Relay client (`RelayClient`) for symmetric NAT | ✅ Phase 2 NNNN | ✅ Phase 2 NNNN |
| Code-signed installer (.pkg / DMG / TestFlight) | ⏸ Phase 2 EEEE | ⏸ Phase 2 EEEE |

## Risks

- **Apple entitlement approval** is the largest external schedule risk.
  Submit the Network Extension capability request as soon as the Apple
  team is enrolled; do not wait for code-side milestones.
- The XCFramework path from Go (`clients/core`) to Swift requires
  [gomobile](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile) or a
  static C library + manual bindings. The current scaffolding side-steps
  this by going through the controller's REST bridge instead.
- macOS / iOS minor version drift can break NetworkExtension behaviour;
  CI should build against the current and current-1 OS releases once
  the integration tests run on Apple hardware.

## Tracking

- [ADR 0012 — Phase 2 Transition](../../docs/adr/0012-phase-2-transition.md)
- [ADR 0011 — Client Core Re-licensing Path](../../docs/adr/0011-client-core-relicensing-path.md)
