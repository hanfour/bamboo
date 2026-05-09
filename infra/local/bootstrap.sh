#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# One-shot bootstrap for the local docker-compose stack. Idempotent —
# safe to re-run after `make local-up` even if you forgot whether you
# already bootstrapped.
#
#   1. Wait for Postgres to be ready
#   2. Run controller migrations
#   3. Create the "default" tenant (idempotent via GetOrCreate)
#   4. Mint a reusable preauth-key for the default tenant
#   5. Print the URLs the operator needs

set -euo pipefail

cd "$(dirname "$0")/../.."

# Resolve binary paths the developer can override.
BAMBOO_CONTROLLER="${BAMBOO_CONTROLLER:-./bin/controller}"
DATABASE_URL="${DATABASE_URL:-postgres://bamboo:dev@localhost:15432/bamboo?sslmode=disable}"
CONTROLLER_HTTP="${CONTROLLER_HTTP:-http://localhost:8081}"
CONTROLLER_GRPC="${CONTROLLER_GRPC:-localhost:8080}"

# 1) Wait for Postgres. The compose health check is the canonical signal,
# but bootstrap.sh might be invoked outside compose (e.g. when iterating
# on the controller binary directly).
echo "==> waiting for Postgres at ${DATABASE_URL%%\?*}"
for i in $(seq 1 60); do
  if docker compose -f infra/local/docker-compose.yml exec -T postgres \
       pg_isready -U bamboo -d bamboo >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

# 2) Migrations. The controller binary embeds them; running migrate up
# is safe to repeat (goose tracks applied versions).
echo "==> running migrations"
DATABASE_URL="${DATABASE_URL}" "${BAMBOO_CONTROLLER}" migrate up

# 3) Wait for the controller's HTTP listener so the rest of the steps
# can drive it via REST.
echo "==> waiting for controller HTTP at ${CONTROLLER_HTTP}"
for i in $(seq 1 60); do
  if curl -sf "${CONTROLLER_HTTP}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

# 4) Mint a preauth-key via grpcurl. We prefer grpcurl because the
# controller's REST surface intentionally does not expose key
# minting (admin-only).
if ! command -v grpcurl >/dev/null 2>&1; then
  cat <<EOF
==> grpcurl not installed; skipping preauth-key generation.

Install via:
  go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

Then run this script again. You can also generate a key manually:
  ./bin/controller ... # or use the Web UI once OIDC is configured.
EOF
else
  echo "==> minting preauth-key"
  KEY=$(grpcurl -plaintext \
    -H "x-tenant-slug: default" \
    -d '{"reusable":true,"ttl_seconds":86400,"description":"local-bootstrap"}' \
    "${CONTROLLER_GRPC}" bamboo.v1.AuthService/CreatePreAuthKey 2>/dev/null \
    | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p')
  if [ -z "$KEY" ]; then
    echo "    ERROR: failed to mint preauth-key. Check controller logs."
    exit 1
  fi
fi

cat <<EOF

================================================================
bamboo local stack ready
================================================================
Controller HTTP:  ${CONTROLLER_HTTP}
Controller gRPC:  ${CONTROLLER_GRPC}
Web UI:           http://localhost:3000
Relay (WS):       ws://localhost:18443/relay
EOF

if [ -n "${KEY:-}" ]; then
  cat <<EOF
Preauth key:      ${KEY}
                  (use --auth-key on the CLI, or paste into the
                   "Pre-auth key" field in the Apple app)
EOF
fi

cat <<EOF

To test from Mac/iPhone over the LAN:
  - Find your Mac's LAN IP:  ipconfig getifaddr en0
  - In the Apple app's Settings, set
      Controller URL: http://<your-LAN-IP>:8081
      Tenant slug:    default
      Pre-auth key:   ${KEY:-<paste from above>}
EOF
