# clients/macos

macOS native client. SwiftUI menu-bar app + a separate
`PacketTunnelProvider` NetworkExtension target that runs the actual
WireGuard tunnel.

**License:** Apache 2.0 — see [LICENSE-APACHE](../../LICENSE-APACHE).

## Status

Pre-alpha skeleton. The Swift sources here compile into a non-functional
shell:

- Menu bar icon + popover with Connect/Disconnect/Quit
- `ConnectionViewModel` is a stub (no controller traffic yet)
- `PacketTunnelProvider` acknowledges start/stop but does not bring up
  a tunnel

The remaining work to make this real is tracked in
[Sprint 2 issue #16](https://github.com/hanfour/bamboo/issues/16).

## Repository layout

```
clients/macos/
  project.yml                                 XcodeGen project specification
  BambooApp/
    BambooApp.swift                           menu-bar entry point
    ContentView.swift                         popover UI
    Info.plist
    BambooApp.entitlements
  PacketTunnelProvider/
    PacketTunnelProvider.swift                NEPacketTunnelProvider stub
    Info.plist
    PacketTunnelProvider.entitlements
```

We deliberately do **not** commit `bamboo.xcodeproj`. Xcode rewrites it
on every settings change, which makes diff review and merge resolution
unproductive. We use [XcodeGen](https://github.com/yonaskolb/XcodeGen)
as the source of truth.

## Apple-side prerequisites (one-off)

These are not automatable from a script — they require an Apple
Developer Program account ($99/year) and a real human in the developer
portal.

1. Enroll in Apple Developer Program (or use an existing team).
2. In `developer.apple.com`, request the **Network Extension** capability
   for your team. Apple reviews this manually; allow 1–3 business days.
3. Create the App ID (`com.example.bamboo.app`) and the App Group your
   menu-bar app and tunnel extension will share.
4. Generate signing certificates / enable Automatic Signing in Xcode.

Set `BAMBOO_DEVELOPMENT_TEAM` in your environment to your Apple Team ID
before generating the project.

## Local build (after the Apple steps above)

```bash
brew install xcodegen
cd clients/macos
xcodegen generate                                # creates bamboo.xcodeproj
open bamboo.xcodeproj                            # build + run from Xcode
```

To bring up the tunnel for the first time you must accept the system
prompt that allows bamboo to add a VPN configuration; this lives in
`System Settings → VPN`.

## Why a separate PacketTunnelProvider target?

macOS only allows the kernel to hand packets to a process that:

- Holds the `com.apple.developer.networking.networkextension` entitlement
- Is registered as a `NEPacketTunnelProvider`
- Runs in its own bundled extension (sandboxed XPC service)

The user-facing menu-bar app cannot satisfy these constraints because
sandboxing rules forbid raw packet access from regular UI apps. The
extension is short-lived and does only the tunnelling; UI lives in the
host app and talks to the extension through `NEVPNConnection`.

## What's wired vs deferred

| Capability | Status |
| --- | --- |
| Menu bar icon, popover UI | ✅ stubbed in Swift |
| Connect / Disconnect buttons | ✅ stubbed (no controller traffic yet) |
| App ↔ Extension messaging via `NEVPNConnection` | ⏸ Sprint 2 #16 |
| Controller registration via gRPC | ⏸ depends on `BambooCore` XCFramework |
| WireGuard tunnel via wireguard-go | ⏸ Sprint 2 #16 |
| OIDC web flow (open browser, intercept callback) | ⏸ ADR 0010 follow-up |
| Code-signed installer (.pkg / DMG) | ⏸ Phase 2 |

## Risks

- **Apple entitlement approval** is the single largest schedule risk in
  Phase 1. Submit early; do not wait until #16 starts.
- The XCFramework path from Go (`clients/core`) to Swift requires
  [gomobile](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile) or a
  static C library + manual bindings. We will choose in a follow-up ADR.
- macOS minor version drift can break NetworkExtension behaviour;
  CI should test against the current and current-1 macOS releases once
  the integration tests run on macOS hardware.

## Tracking

- [Sprint 2 — Issue #16](https://github.com/hanfour/bamboo/issues/16)
- [ADR 0011 — Client Core Re-licensing Path](../../docs/adr/0011-client-core-relicensing-path.md)
