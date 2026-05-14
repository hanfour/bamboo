#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Build libwg-go.a from the WireGuardKitGo Swift Package target before
# the linker on the parent target (BambooApp-* or PacketTunnelProvider-*)
# resolves `-lwg-go`.
#
# WireGuardKit's `WireGuardKitGo` SPM target ships only Go sources + a
# Makefile that produces `libwg-go.a` via cgo cross-compilation. SPM does
# not run Makefiles, so the static lib never exists at link time unless
# a build phase produces it. This script does that.
#
# Quirk: the upstream Makefile uses `$@` and `$^` Make variables which
# do not survive paths containing spaces. DerivedData on machines like
# `/Volumes/SATECHI DISK Media/...` has spaces, breaking Make's target
# tokenisation. Workaround: redirect Make's BUILDDIR + DESTDIR into a
# space-free scratch tree under `~/.cache/bamboo-wggo/`, then copy the
# final lipo'd `libwg-go.a` back to where Xcode's linker expects it
# (CONFIGURATION_BUILD_DIR). The lipo `cp` at the end handles spaces.
#
# Required environment (Xcode passes these automatically when invoked as
# a build phase):
#   - BUILD_DIR             Build/Products parent dir (used to locate SPM checkout)
#   - ARCHS                 e.g. "arm64" or "arm64 x86_64"
#   - PLATFORM_NAME         "macosx" / "iphoneos" / "iphonesimulator"
#   - SDKROOT               full path to the SDK
#   - CONFIGURATION_BUILD_DIR     dir Xcode's linker will look in for libwg-go.a
#
# Required developer tooling: `go` in PATH (install via `brew install go`).

set -euo pipefail

# Allow xcodegen-managed Run Script with no input files to skip Xcode's
# dependency-analysis short-circuit warning quietly.
: "${SCRIPT_INPUT_FILE_COUNT:=0}"

# Xcode build phases inherit a stripped PATH (typically just /usr/bin:
# /bin:/usr/sbin:/sbin) and do NOT see Homebrew or asdf paths the user's
# interactive shell adds. Inject the standard Homebrew binaries the
# WireGuard Makefile also expects so `go` resolves regardless of how
# Xcode was launched.
export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin"

if ! command -v go >/dev/null 2>&1; then
    cat >&2 <<EOF
error: 'go' is not in PATH.

Install Go to build the WireGuardKit Go bridge:

    brew install go

The WireGuard upstream Package.swift requires the Go bridge be built
from source (no precompiled libwg-go.a is shipped via SPM). One-time
setup per developer Mac. See bamboo PR #105 / #106 history for context.
EOF
    exit 1
fi

# Locate the SPM-resolved checkout. DerivedData layout varies between
# Build and Archive modes:
#
#   Build:    BUILD_DIR = .../DerivedData/<proj>/Build/Products
#   Archive:  BUILD_DIR = .../DerivedData/<proj>/Build/Intermediates.noindex/
#                          ArchiveIntermediates/<target>/BuildProductsPath
#
# Both layouts share .../DerivedData/<proj>/SourcePackages/checkouts/.
# Walk up from BUILD_DIR until we find a sibling SourcePackages dir.
SP_ROOT=""
candidate="$BUILD_DIR"
while [[ "$candidate" != "/" && "$candidate" != "." ]]; do
    if [[ -d "$candidate/SourcePackages/checkouts/wireguard-apple" ]]; then
        SP_ROOT="$candidate"
        break
    fi
    candidate=$(dirname "$candidate")
done

if [[ -z "$SP_ROOT" ]]; then
    echo "error: could not find SourcePackages/checkouts/wireguard-apple anywhere above:" >&2
    echo "  $BUILD_DIR" >&2
    echo "" >&2
    echo "Run 'xcodebuild -resolvePackageDependencies' once so SPM" >&2
    echo "materializes the WireGuard checkout, then rebuild." >&2
    exit 1
fi

WGGO_DIR="$SP_ROOT/SourcePackages/checkouts/wireguard-apple/Sources/WireGuardKitGo"

# Re-route Make's BUILDDIR + DESTDIR into a space-free scratch tree so
# the upstream Makefile's $@/$^ token expansion works. Per-(platform,
# arch-set, configuration) so concurrent / cross-platform builds don't
# clobber each other.
ARCH_TAG="${ARCHS// /-}"
SAFE_ROOT="${HOME}/.cache/bamboo-wggo/${PLATFORM_NAME}-${ARCH_TAG}-${CONFIGURATION:-Debug}"
SAFE_BUILD_DIR="${SAFE_ROOT}/build"
SAFE_TEMP_DIR="${SAFE_ROOT}/temp"
mkdir -p "$SAFE_BUILD_DIR" "$SAFE_TEMP_DIR"

echo "==> Building libwg-go.a for arch(s): ${ARCHS} platform: ${PLATFORM_NAME}"
echo "    scratch tree: $SAFE_ROOT"

# The Makefile reads ARCHS / PLATFORM_NAME / SDKROOT from Xcode and
# CONFIGURATION_{BUILD,TEMP}_DIR via env. Make's dependency tracking
# keeps incremental builds ~0s; clean builds cost ~30s for the cgo
# cross-compile.
env \
    CONFIGURATION_BUILD_DIR="$SAFE_BUILD_DIR" \
    CONFIGURATION_TEMP_DIR="$SAFE_TEMP_DIR" \
    make -C "$WGGO_DIR" build

# Copy the result to where Xcode's linker will look. Quoted paths
# survive the space in CONFIGURATION_BUILD_DIR.
SRC="$SAFE_BUILD_DIR/libwg-go.a"
DST="${CONFIGURATION_BUILD_DIR}/libwg-go.a"

if [[ ! -f "$SRC" ]]; then
    echo "error: Makefile completed but $SRC is missing" >&2
    exit 1
fi

mkdir -p "${CONFIGURATION_BUILD_DIR}"
cp "$SRC" "$DST"
echo "==> $(file "$DST" | head -1)"
