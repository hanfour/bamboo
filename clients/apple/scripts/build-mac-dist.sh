#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# build-mac-dist.sh — produce a signed, copy-deployable macOS .app
# you can scp to a second Mac instead of re-running xcodegen + Xcode
# there. Useful for internal multi-Mac deploys before you have a
# notarized Developer-ID build pipeline.
#
# What it does:
#   1. xcodegen generate                                  (deterministic project)
#   2. xcodebuild build -configuration Release            (Apple Development signing)
#   3. ditto -c -k --keepParent bamboo.app dist/bamboo-mac.zip   (preserve symlinks + xattrs)
#
# Output: clients/apple/dist/bamboo-mac.zip — extract anywhere.
#
# Caveat: the embedded extension is signed with Apple Development
# (not Developer ID Application), so the destination Mac needs to
# either be signed into the same Apple Developer team OR you trust
# the cert manually (Settings → Privacy & Security after first launch).
# Gatekeeper will quarantine the unzipped .app on the destination;
# strip the attribute once:
#
#   xattr -dr com.apple.quarantine /Applications/bamboo.app
#
# For App Store / public distribution build a notarized Developer-ID
# archive instead (out of scope for this internal-dist script).

set -euo pipefail

# Run from clients/apple regardless of caller's cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

SCHEME="${SCHEME:-BambooApp-macOS}"
# Release config + Developer ID Application signing is what produces a
# .app that other Macs can install without disabling SIP. Apple
# Development signing only works on machines registered to the same
# team AND requires `systemextensionsctl developer on` (SIP must be
# off) — not useful for dist. Override CONFIG=Debug if you specifically
# need Xcode-cmd+R parity.
CONFIG="${CONFIG:-Release}"

# Apple Developer Team ID. project.yml deliberately omits this so Xcode
# GUI picks it from your signed-in Apple ID, but the xcodebuild CLI
# can't ask interactively — it needs the team explicitly. Override via
# BAMBOO_TEAM_ID if you're forking.
TEAM_ID="${BAMBOO_TEAM_ID:-UK48R5KWLV}"

# Developer ID Application signing identity. Must be present in the
# login keychain. Apple mints these via developer.apple.com →
# Certificates → "Developer ID Application". See clients/apple/docs
# for the bootstrap steps (CSR + cert download + p12 import).
SIGN_IDENTITY="${SIGN_IDENTITY:-Developer ID Application: PoHan Huang (UK48R5KWLV)}"

# Manual provisioning profile specifiers. The Developer ID flow
# requires per-bundle-ID profiles that grant the
# *-systemextension entitlement values; xcodebuild can't auto-mint
# Developer ID profiles, so we install them ahead of time under
# ~/Library/MobileDevice/Provisioning Profiles/ and reference by
# the human-readable name shown in the portal.
PROFILE_APP="${PROFILE_APP:-bamboo app DevID}"
PROFILE_TUNNEL="${PROFILE_TUNNEL:-bamboo tunnel DevID}"
PROFILE_DNSPROXY="${PROFILE_DNSPROXY:-bamboo dnsproxy DevID}"
DERIVED_DIR="${DERIVED_DIR:-$HOME/.cache/bamboo-mac-dist}"
DIST_DIR="$(pwd)/dist"
OUT_ZIP="$DIST_DIR/bamboo-mac.zip"

for cmd in xcodegen xcodebuild ditto go; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "error: '$cmd' not in PATH" >&2
        echo "    brew install xcodegen go" >&2
        exit 1
    fi
done

echo "==> xcodegen generate"
xcodegen generate

echo "==> xcodebuild build (scheme=$SCHEME, config=$CONFIG)"
mkdir -p "$DERIVED_DIR"
# Per-target signing identity + provisioning profile names live in
# project.yml's per-config settings blocks (Release config). Only
# the team and runtime-hardening flag are passed here.
xcodebuild build \
    -project bamboo.xcodeproj \
    -scheme "$SCHEME" \
    -configuration "$CONFIG" \
    -derivedDataPath "$DERIVED_DIR" \
    -destination 'generic/platform=macOS' \
    DEVELOPMENT_TEAM="$TEAM_ID" \
    OTHER_CODE_SIGN_FLAGS="--options=runtime --timestamp" \
    CODE_SIGN_INJECT_BASE_ENTITLEMENTS=NO \
    | tail -20

APP_PATH="$DERIVED_DIR/Build/Products/$CONFIG/bamboo.app"
if [[ ! -d "$APP_PATH" ]]; then
    echo "error: build finished but $APP_PATH is missing" >&2
    exit 1
fi

# System Extension sanity-check — both tunnel and dnsproxy live under
# Contents/Library/SystemExtensions/ as .systemextension bundles on
# macOS 15+. The wrapper directory name must equal the bundle
# identifier (PRODUCT_NAME is set to the identifier in project.yml)
# because sysextd looks for ${identifier}.systemextension when
# realizing the extension; a PRODUCT_NAME like "bamboo-dnsproxy"
# produces a wrapper sysextd can't find.
SYSEXT_DIR="$APP_PATH/Contents/Library/SystemExtensions"
for ext in dev.hanfour.bamboo.app.tunnel.systemextension dev.hanfour.bamboo.app.dnsproxy.systemextension; do
    EXT_INFO="$SYSEXT_DIR/$ext/Contents/Info.plist"
    if [[ ! -f "$EXT_INFO" ]]; then
        echo "error: $EXT_INFO missing — extension wasn't embedded" >&2
        exit 1
    fi
    if [[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundlePackageType' "$EXT_INFO" 2>/dev/null)" != "SYSX" ]]; then
        echo "error: $ext CFBundlePackageType is not SYSX — sysextd won't see it" >&2
        exit 1
    fi
done

echo "==> packaging"
mkdir -p "$DIST_DIR"
rm -f "$OUT_ZIP"
# ditto -c -k --sequesterRsrc --keepParent  preserves extended attributes
# (notarization tickets, code-signing supplemental info) which `zip` strips.
ditto -c -k --sequesterRsrc --keepParent "$APP_PATH" "$OUT_ZIP"

SIZE=$(du -h "$OUT_ZIP" | cut -f1)
echo
echo "==> Done: $OUT_ZIP ($SIZE)"
echo
echo "Copy to another Mac:"
echo "    scp '$OUT_ZIP' <user>@<host>:~/Downloads/"
echo
echo "On the destination Mac:"
echo "    cd /Applications && unzip ~/Downloads/bamboo-mac.zip"
echo "    xattr -dr com.apple.quarantine /Applications/bamboo.app"
echo "    open /Applications/bamboo.app"
