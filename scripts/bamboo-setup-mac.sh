#!/bin/bash
# bamboo path-A: register this Mac as a WireGuard peer manually.
# Generates a keypair, registers with bamboo, adds the Mac peer to
# the VPS's wg interface, and writes a .conf for the WireGuard app.

set -euo pipefail

CONTROLLER_URL="${CONTROLLER_URL:-https://bamboo.miilink.net}"
VPS_IP="${VPS_IP:-54.238.9.51}"
VPS_WG_PORT="${VPS_WG_PORT:-51820}"
VPS_PUBKEY="${VPS_PUBKEY:-w+3xhUb6yQ7+lXiL0U9EYq+xPJJJCRnVHiWdISvgpnQ=}"
VPS_TUN_IP="${VPS_TUN_IP:-100.64.0.1}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/bamboo-lightsail.pem}"
HOSTNAME_LABEL="${HOSTNAME_LABEL:-$(hostname -s)}"
OUT="${OUT:-$HOME/Desktop/bamboo-mac.conf}"

# 1) Generate Curve25519 keypair locally via openssl (preinstalled
# on macOS). Both raw 32-byte keys are exactly what WireGuard wants.
echo "==> generating WireGuard keypair locally"
TMP_PRIV=$(mktemp)
openssl genpkey -algorithm X25519 -out "$TMP_PRIV" 2>/dev/null
PRIV=$(openssl pkey -in "$TMP_PRIV" -outform DER 2>/dev/null \
  | tail -c 32 | base64)
PUB=$(openssl pkey -in "$TMP_PRIV" -pubout -outform DER 2>/dev/null \
  | tail -c 32 | base64)
rm -f "$TMP_PRIV"
echo "    pubkey: $PUB"

# 2) Register with bamboo. Server allocates an IP; response includes
# every other peer in the tenant.
echo "==> registering with bamboo controller"
RESP=$(curl -sk --max-time 10 \
  -X POST "$CONTROLLER_URL/api/v1/peers/register" \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Slug: default" \
  -d "{\"hostname\":\"$HOSTNAME_LABEL\",\"wireguardPublicKey\":\"$PUB\",\"os\":\"darwin\",\"clientVersion\":\"manual-wg\",\"tenantSlug\":\"default\"}")
SELF_IP=$(echo "$RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['self']['ip'])")
SELF_ID=$(echo "$RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['self']['id'])")
echo "    assigned: $SELF_IP  (peer id $SELF_ID)"

# 3) Add this Mac as a WireGuard peer on the VPS so the VPS's wg
# kernel module knows to accept handshakes from this pubkey.
# Initiator pattern: the Mac dials VPS, so VPS does not need an
# endpoint for the Mac.
echo "==> adding Mac peer on VPS"
ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new \
    "ubuntu@$VPS_IP" \
    "sudo wg set bamboo0 peer '$PUB' allowed-ips '$SELF_IP/32'"
echo "    done"

# 4) Write a wg-quick conf that the WireGuard macOS / iOS app can
# import directly.
echo "==> writing $OUT"
cat > "$OUT" <<EOF
# bamboo manual-WG config for $HOSTNAME_LABEL.
# Generated $(date -u +%Y-%m-%dT%H:%M:%SZ).
[Interface]
Address    = $SELF_IP/32
PrivateKey = $PRIV
DNS        = 1.1.1.1

[Peer]
# vps-tokyo (bamboo VPS at $VPS_IP)
PublicKey           = $VPS_PUBKEY
Endpoint            = $VPS_IP:$VPS_WG_PORT
AllowedIPs          = $VPS_TUN_IP/32
PersistentKeepalive = 25
EOF
chmod 600 "$OUT"

cat <<EOF

================================================================
done.
================================================================

WireGuard config written to:
    $OUT

Next:
  1. Open the WireGuard app (App Store).
  2. Click "+" -> "Import tunnel(s) from file" -> pick
       $OUT
  3. Click "Activate".
  4. Test: ping $VPS_TUN_IP

You should see latency ~50-80ms (Tokyo from Taiwan).

For iPhone:
  - Save the same config as a QR-code-friendly form. Easiest:
      brew install qrencode
      qrencode -t ansiutf8 < $OUT
    then scan from the WireGuard iPhone app's "+" -> "Create from QR code".

Mesh peers:
    Mac       100.64.0.2  (this device)
    VPS       100.64.0.1
EOF
