#!/bin/sh
# bamboo client installer for Linux, served by the controller at /install.sh.
#
#   curl -fsSL https://bamboo.example.com/install.sh | sudo env BAMBOO_AUTH_KEY=bka_... sh
#
# Downloads the bamboo CLI from the project's GitHub release, checks its
# SHA-256, installs it to /usr/local/bin and, when BAMBOO_AUTH_KEY is set,
# registers this machine and starts it as a systemd service.
#
# Environment:
#   BAMBOO_AUTH_KEY   pre-auth key from the Web UI (Peers, Add peer)
#   BAMBOO_HOSTNAME   peer name (default: this host's name)
#   BAMBOO_SERVER     controller URL (default: the server this script came from)
#   BAMBOO_VERSION    release tag such as v0.1.17 (default: latest)
#   BAMBOO_NO_SERVICE set to 1 to install the binary only
set -eu

SERVER="${BAMBOO_SERVER:-__BAMBOO_SERVER__}"
VERSION="${BAMBOO_VERSION:-latest}"
REPO="hanfour/bamboo"
BIN_DIR="/usr/local/bin"

say() { printf '%s\n' "$*"; }
die() { printf 'bamboo install: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "this installer supports Linux only. On macOS use the app under clients/apple."
[ "$(id -u)" -eq 0 ] || die "run as root, for example: curl -fsSL $SERVER/install.sh | sudo env BAMBOO_AUTH_KEY=... sh"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"
case "$SERVER" in https://*|http://*) ;; *) die "no controller URL; set BAMBOO_SERVER=https://your-controller" ;; esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

if [ "$VERSION" = "latest" ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi
ASSET="bamboo-linux-$ARCH"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

say "downloading $ASSET ($VERSION)"
curl -fsSL -o "$TMP/$ASSET" "$BASE/$ASSET" || die "download failed: $BASE/$ASSET"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" || die "download failed: $BASE/SHA256SUMS"

WANT="$(grep " $ASSET\$" "$TMP/SHA256SUMS" | cut -d' ' -f1)"
[ -n "$WANT" ] || die "$ASSET is not listed in SHA256SUMS"
GOT="$(sha256sum "$TMP/$ASSET" | cut -d' ' -f1)"
[ "$WANT" = "$GOT" ] || die "checksum mismatch for $ASSET (expected $WANT, got $GOT)"

install -m 0755 "$TMP/$ASSET" "$BIN_DIR/bamboo"
say "installed $BIN_DIR/bamboo ($("$BIN_DIR/bamboo" version | head -1))"

# WireGuard is in the kernel from Linux 5.6. Try to load it, and say so if not.
if [ ! -d /sys/module/wireguard ]; then
  modprobe wireguard >/dev/null 2>&1 || say "warning: the wireguard kernel module is not available; install wireguard or a newer kernel"
fi

if [ "${BAMBOO_NO_SERVICE:-0}" = "1" ]; then
  say "skipping the service. Start it with: sudo env BAMBOO_AUTH_KEY=... bamboo up --server $SERVER"
  exit 0
fi
if [ -z "${BAMBOO_AUTH_KEY:-}" ]; then
  say "no BAMBOO_AUTH_KEY set, so this machine is not registered yet. Create a key in the Web UI, then run:"
  say "  sudo env BAMBOO_AUTH_KEY=bka_... bamboo install-service --server $SERVER"
  exit 0
fi

# The key travels in the environment, not on the command line.
export BAMBOO_SERVER="$SERVER"
"$BIN_DIR/bamboo" install-service
say "done. This machine should appear under Peers within a few seconds."
