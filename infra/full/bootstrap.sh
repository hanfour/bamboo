#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Run this ONCE on the VPS after `docker compose up -d`. It:
#   1. Waits for the controller HTTP listener to be ready
#   2. Runs schema migrations
#   3. Mints a reusable preauth-key for the default tenant
#   4. Prints the URLs + key the operator needs
#
# Re-running is safe (idempotent) — migrations track applied versions
# and preauth-keys are cheap to mint multiple times.

set -euo pipefail

cd "$(dirname "$0")"

# Sanity-check .env exists.
if [ ! -f .env ]; then
    echo "ERROR: .env is missing. Copy .env.example to .env and fill in."
    exit 2
fi

# shellcheck disable=SC1091
set -a; source .env; set +a

CONTROLLER_LOCAL_HTTP="${CONTROLLER_LOCAL_HTTP:-http://localhost:8081}"

# 1) Allow inter-peer forwarding when the host runs WireGuard as a
# DERP-style hub (path-A / manual-WG dogfood). Without this, Ubuntu's
# default FORWARD policy DROP silently breaks A↔hub↔B traffic between
# tunnel peers. Idempotent: the duplicate-check skips when the rule
# already exists. Persisted via iptables-persistent so reboots keep
# it. No-op if the bamboo0 interface doesn't exist yet (controller-
# only deploys); the rule attaches by interface name and is harmless
# until WG is also configured.
if command -v iptables >/dev/null 2>&1; then
    if ! sudo iptables -C FORWARD -i bamboo0 -o bamboo0 -j ACCEPT 2>/dev/null; then
        echo "==> adding iptables FORWARD rule for bamboo0 hub forwarding"
        sudo iptables -I FORWARD 1 -i bamboo0 -o bamboo0 -j ACCEPT
        if ! dpkg -s iptables-persistent >/dev/null 2>&1; then
            echo "iptables-persistent iptables-persistent/autosave_v4 boolean true" \
                | sudo debconf-set-selections
            echo "iptables-persistent iptables-persistent/autosave_v6 boolean true" \
                | sudo debconf-set-selections
            sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
                iptables-persistent >/dev/null
        fi
        sudo netfilter-persistent save >/dev/null
    fi
fi

# 2) Run migrations via the controller container itself. We need the
# embedded migration files which live in the image; running migrate
# in a one-shot container is the simplest path.
echo "==> running migrations"
docker compose run --rm \
    -e DATABASE_URL="postgres://bamboo:${POSTGRES_PASSWORD}@postgres:5432/bamboo?sslmode=disable" \
    controller migrate up

# 2) Wait for controller HTTP. We probe over Caddy because that's
# the path the apps actually use; the cert may take a few seconds
# to issue on first boot.
echo "==> waiting for https://${DOMAIN}/healthz"
for i in $(seq 1 60); do
    if curl -sfk "https://${DOMAIN}/healthz" >/dev/null 2>&1; then
        break
    fi
    sleep 2
done

# 3) Mint a preauth-key. We use grpcurl from inside the controller
# container because the gRPC port isn't exposed externally.
if docker compose exec -T controller test -x /bin/grpcurl 2>/dev/null; then
    GRPCURL="docker compose exec -T controller /bin/grpcurl"
elif command -v grpcurl >/dev/null 2>&1; then
    GRPCURL="grpcurl"
else
    cat <<EOF
==> grpcurl not available. Install on the VPS:
    go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

    or skip this step and create your first peer via OIDC sign-in
    once you've configured Google / GitHub OAuth.
EOF
    GRPCURL=""
fi

KEY=""
if [ -n "$GRPCURL" ]; then
    echo "==> minting preauth-key"
    KEY=$($GRPCURL -plaintext \
        -H "x-tenant-slug: default" \
        -d '{"reusable":true,"ttl_seconds":2592000,"description":"vps-bootstrap"}' \
        controller:8080 bamboo.v1.AuthService/CreatePreAuthKey 2>/dev/null \
        | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' || true)
fi

cat <<EOF

================================================================
bamboo VPS deployment ready
================================================================
Web UI:           https://${DOMAIN}
Controller HTTP:  https://${DOMAIN}/api/v1/...
OIDC sign-in:     https://${DOMAIN}/auth/google/login
Relay (WS):       wss://${RELAY_DOMAIN}/relay
EOF

if [ -n "$KEY" ]; then
    cat <<EOF
Preauth key:      ${KEY}
                  (TTL 30 days, reusable)
EOF
fi

cat <<EOF

In the bamboo app on Mac / iPhone:
  Controller URL:  https://${DOMAIN}
  Tenant slug:     default
  Pre-auth key:    ${KEY:-(see above; or sign in via Google/GitHub)}
  Relay URL:       wss://${RELAY_DOMAIN}/relay
EOF
