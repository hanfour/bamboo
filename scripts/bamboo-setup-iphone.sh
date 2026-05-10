#!/bin/bash
# bamboo path-A: provision a NEW iPhone peer.
# Run this on a Mac (or any machine with curl + openssl + ssh access
# to the bamboo VPS). Produces a wg-quick conf and prints it as a QR
# code on the terminal so the WireGuard iPhone app can scan it.

set -euo pipefail

CONTROLLER_URL="${CONTROLLER_URL:-https://bamboo.miilink.net}"
VPS_IP="${VPS_IP:-54.238.9.51}"
VPS_WG_PORT="${VPS_WG_PORT:-51820}"
VPS_PUBKEY="${VPS_PUBKEY:-w+3xhUb6yQ7+lXiL0U9EYq+xPJJJCRnVHiWdISvgpnQ=}"
VPS_TUN_IP="${VPS_TUN_IP:-100.64.0.1}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/bamboo-lightsail.pem}"
HOSTNAME_LABEL="${HOSTNAME_LABEL:-iphone}"
OUT="${OUT:-$HOME/Desktop/bamboo-iphone.conf}"

# 0) Make sure qrencode is available; install via brew if not.
if ! command -v qrencode >/dev/null 2>&1; then
  echo "==> qrencode not found; installing via Homebrew"
  if ! command -v brew >/dev/null 2>&1; then
    echo "ERROR: Homebrew is not installed. Install brew first:"
    echo "    /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
    exit 1
  fi
  brew install qrencode
fi

# 1) Generate a fresh Curve25519 keypair locally for the iPhone.
echo "==> generating WireGuard keypair for iPhone"
TMP_PRIV=$(mktemp)
openssl genpkey -algorithm X25519 -out "$TMP_PRIV" 2>/dev/null
PRIV=$(openssl pkey -in "$TMP_PRIV" -outform DER 2>/dev/null \
  | tail -c 32 | base64)
PUB=$(openssl pkey -in "$TMP_PRIV" -pubout -outform DER 2>/dev/null \
  | tail -c 32 | base64)
rm -f "$TMP_PRIV"
echo "    pubkey: $PUB"

# 2) Register the iPhone with bamboo. Server allocates a fresh IP
# from the tenant pool.
echo "==> registering with bamboo controller"
RESP=$(curl -sk --max-time 10 \
  -X POST "$CONTROLLER_URL/api/v1/peers/register" \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: default" \
  -d "{\"hostname\":\"$HOSTNAME_LABEL\",\"wireguardPublicKey\":\"$PUB\",\"os\":\"ios\",\"clientVersion\":\"manual-wg\",\"tenantSlug\":\"default\"}")
SELF_IP=$(echo "$RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['self']['ip'])")
SELF_ID=$(echo "$RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['self']['id'])")
echo "    assigned: $SELF_IP  (peer id $SELF_ID)"

# 3) Add the iPhone as a WireGuard peer on the VPS so the kernel
# accepts handshakes from this pubkey.
echo "==> adding iPhone peer on VPS"
ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new \
    "ubuntu@$VPS_IP" \
    "sudo wg set bamboo0 peer '$PUB' allowed-ips '$SELF_IP/32'"
echo "    done"

# 4) Build the wg-quick conf.
cat > "$OUT" <<EOF
# bamboo manual-WG config for iPhone.
# Generated $(date -u +%Y-%m-%dT%H:%M:%SZ).
[Interface]
Address    = $SELF_IP/32
PrivateKey = $PRIV
DNS        = 1.1.1.1

[Peer]
# vps-tokyo (bamboo VPS at $VPS_IP)
PublicKey           = $VPS_PUBKEY
Endpoint            = $VPS_IP:$VPS_WG_PORT
AllowedIPs          = 100.64.0.0/24
PersistentKeepalive = 25
EOF
chmod 600 "$OUT"
echo "==> wrote $OUT"

# 5) Print the conf as a QR code that the iPhone WireGuard app
# scans directly. ANSI block characters render in any modern
# terminal (light background recommended for best contrast — flip
# colors below if your terminal is dark).
echo ""
echo "================================================================"
echo "QR code below — open WireGuard on iPhone:"
echo "  + (top right)  ->  Create from QR code"
echo "  point camera at this terminal."
echo "================================================================"
echo ""
qrencode -t ansiutf8 < "$OUT"
echo ""
echo "================================================================"
echo "Mesh peers:"
echo "    iPhone    $SELF_IP    (this device, just registered)"
echo "    Mac       100.64.0.3  (your earlier setup)"
echo "    VPS       $VPS_TUN_IP"
echo ""
echo "After connecting, test from iPhone Safari:"
echo "    https://bamboo.miilink.net/peers   <- list all peers"
echo "    or ping 100.64.0.1 from a terminal app like a-Shell"
echo "================================================================"
