# Apple Client Test Walkthrough

End-to-end recipe for taking a clean clone of `hanfour/bamboo` and
running the bamboo macOS + iOS clients on your own Mac and iPhone
against a local controller. Targets: Phase 2 theme 4.

This walkthrough assumes a paid Apple Developer Program account and
macOS with Xcode 15+.

## 0. One-off Apple-side setup

These steps require a real human in `developer.apple.com` and cannot
be automated.

1. **Enroll** in Apple Developer Program ($99 / year).
2. In `developer.apple.com → Certificates, Identifiers, & Profiles`,
   request the **Network Extension** capability for your team. Apple
   reviews this manually; allow 1–3 business days. Until it is
   approved you can still build but startVPNTunnel will fail at
   runtime with the system claiming the entitlement is unauthorized.
3. Create the App IDs:
   - `dev.hanfour.bamboo.app`        (macOS host app + iOS host app)
   - `dev.hanfour.bamboo.app.tunnel` (macOS extension + iOS extension)
   Both need the **Network Extensions** capability checked, plus an
   **App Group** (e.g. `group.dev.hanfour.bamboo`) shared between the
   four IDs once you wire keychain-sharing in a future PR.
4. Make sure your team's Apple ID is signed into Xcode → Settings →
   Accounts.

Substitute `dev.hanfour.bamboo` for your own prefix if you forked the
project; the rest of this document uses the default.

## 1. Required environment variables

```bash
export BAMBOO_DEVELOPMENT_TEAM=ABCDE12345         # your 10-char Team ID
export BAMBOO_BUNDLE_ID_PREFIX=dev.hanfour.bamboo # default; override if forking
```

You can put these in `~/.zshrc` so every shell sees them.

## 2. Bring up the controller

```bash
cd infra
docker compose up -d postgres
cd ..
make build
./bin/controller migrate up -c apps/controller/config/example.yaml
./bin/controller serve -c apps/controller/config/example.yaml
```

Leave this terminal running. The controller's HTTP listener is on
`http://localhost:8081`; the macOS / iOS clients will hit
`/api/v1/peers/register` here.

## 3. Generate the Xcode project

```bash
brew install xcodegen
make apple-generate
open clients/apple/bamboo.xcodeproj
```

Pick the scheme:
- `BambooApp-macOS` — runs as a menu-bar app on this Mac
- `BambooApp-iOS`   — runs on a connected iPhone or the iOS Simulator

## 4. macOS first connect

1. Build + Run `BambooApp-macOS` from Xcode.
2. The bamboo icon appears in the macOS menu bar.
3. Click → ⚙ → set:
   - Controller URL: `http://localhost:8081` (or your machine's LAN IP
     if testing over the network)
   - Tenant slug: `default`
   - Pre-auth key: leave blank for the dev fallback
4. Click **Connect**.
5. macOS prompts you to allow bamboo to add a VPN configuration —
   approve. (The first time only; subsequent connects skip the
   prompt.)
6. Status changes to `Connecting…` → `Connected`.

### Verifying the tunnel is up

```bash
# Inspect the assigned tunnel IP from the controller's logs:
grep "new peer registered" /var/log/...   # or read the controller stdout

# On the Mac, the bamboo0 utun interface should appear:
ifconfig | grep -A 2 utun

# Ping the controller's view of "self" — should succeed (loopback to
# the tunnel address):
ping -c 1 100.64.0.1
```

If the tunnel doesn't come up:
- `Console.app` → search for `dev.hanfour.bamboo.tunnel` to see the
  PacketTunnelProvider's logs.
- Most failures at this stage are entitlement-related: re-check that
  Network Extension is approved on your team and that automatic
  signing succeeded for both targets.

## 5. iOS first connect

1. Connect your iPhone to the Mac via USB or Wi-Fi.
2. Pick `BambooApp-iOS` scheme + your device as the run destination.
3. Build + Run.
4. iOS will prompt to trust the developer certificate — go to
   `Settings → General → VPN & Device Management → Developer App`
   and approve your Apple ID.
5. Open bamboo on the phone. Tap ⚙ and set:
   - Controller URL: `http://<mac-lan-ip>:8081`
     (the iPhone can't reach `localhost` on your Mac)
   - Tenant slug: `default`
6. Tap **Connect**.
7. iOS asks "bamboo would like to add VPN configurations" — approve
   with Face ID / Touch ID.
8. Status changes to `Connecting…` → `Connected`.

### Verifying the tunnel is up on iOS

- Settings → VPN should show `bamboo` with a green Connected dot.
- `Console.app` on the Mac, with the iPhone selected, will show the
  PacketTunnelProvider's logs under `dev.hanfour.bamboo.tunnel`.

## 6. What works today vs. what doesn't

End-to-end at this point:

- ✅ Each device generates its own Curve25519 keypair (Keychain-backed)
- ✅ Each device registers and gets a unique `100.64.0.x` tunnel IP
- ✅ The PacketTunnelProvider brings up a real WireGuardKit tunnel
- ✅ The system VPN status reflects the real connection state
- ✅ Controller's `/api/v1/peers` shows both devices

Doesn't work yet:

- ❌ **Direct peer-to-peer connectivity.** The controller does not yet
  publish reachable endpoints (host:port) for peers, so even though
  both Macs / phones are in each other's allowed-IPs lists, the
  WireGuard handshake has nowhere to dial. STUN + a relay fallback
  are tracked as a Phase 2 follow-up.
- ❌ **OIDC sign-in from the app.** The Settings sheet uses the
  X-Tenant-Slug dev fallback or a pre-auth key. The
  ASWebAuthenticationSession flow lands as a follow-up alongside
  Keychain-shared session tokens.

## 7. Distributing internally via TestFlight

When you want non-developers to test:

1. Bump `MARKETING_VERSION` and `CURRENT_PROJECT_VERSION` in
   `clients/apple/project.yml`.
2. `make apple-generate`.
3. Open the project, pick `BambooApp-iOS` scheme, choose `Any iOS
   Device (arm64)` as destination.
4. `Product → Archive`.
5. In the Organizer window that opens, click `Distribute App` →
   `App Store Connect` → `Upload`.
6. Wait ~10–30 minutes for App Store Connect to process the build.
7. In `appstoreconnect.apple.com → My Apps → bamboo → TestFlight`,
   add internal testers (your Apple ID is auto-included).
8. Testers get a TestFlight notification and can install with one
   tap.

The macOS path is similar but uses `Distribute App → Direct
Distribution → Developer ID` for signed-but-not-App-Store builds.

## 8. Tear down

```bash
# In the apps: Disconnect, then remove the VPN config from
# System Settings → VPN (macOS) / Settings → VPN & Device Management
# (iOS) so the next test starts from a clean slate.

cd infra && docker compose down
```
