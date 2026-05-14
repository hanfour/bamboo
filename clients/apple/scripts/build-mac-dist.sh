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
# Default to Debug: the network-extension entitlement requires an Apple
# Development certificate to sign, and Xcode's Release config wants a
# distribution certificate by default. Cmd+R in Xcode also uses Debug,
# so this matches the binary you already verified locally. If you want
# a Release binary, you need to either notarize a Developer ID build
# or override CODE_SIGN_IDENTITY=Apple Development explicitly.
CONFIG="${CONFIG:-Debug}"

# Apple Developer Team ID. project.yml deliberately omits this so Xcode
# GUI picks it from your signed-in Apple ID, but the xcodebuild CLI
# can't ask interactively — it needs the team explicitly. Override via
# BAMBOO_TEAM_ID if you're forking.
TEAM_ID="${BAMBOO_TEAM_ID:-UK48R5KWLV}"
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
xcodebuild build \
    -project bamboo.xcodeproj \
    -scheme "$SCHEME" \
    -configuration "$CONFIG" \
    -derivedDataPath "$DERIVED_DIR" \
    -destination 'generic/platform=macOS' \
    -allowProvisioningUpdates \
    DEVELOPMENT_TEAM="$TEAM_ID" \
    | tail -20

APP_PATH="$DERIVED_DIR/Build/Products/$CONFIG/bamboo.app"
if [[ ! -d "$APP_PATH" ]]; then
    echo "error: build finished but $APP_PATH is missing" >&2
    exit 1
fi

# Embedded extension sanity-check — without this the destination Mac
# would silently fail at Connect time the way PR #113 fixed.
EXT_INFO="$APP_PATH/Contents/PlugIns/bamboo-tunnel.appex/Contents/Info.plist"
if [[ ! -f "$EXT_INFO" ]]; then
    echo "error: $EXT_INFO missing — extension wasn't embedded" >&2
    exit 1
fi
if ! /usr/libexec/PlistBuddy -c 'Print :NSExtension:NSExtensionPointIdentifier' "$EXT_INFO" 2>/dev/null \
        | grep -q "com.apple.networkextension.packet-tunnel"; then
    echo "error: extension Info.plist missing NSExtension dict — see PR #113" >&2
    exit 1
fi

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
