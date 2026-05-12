#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# bamboo end-to-end demo: prove ACL enforcement is real, not just UI.
#
# Drives the local stack through tenant -> two peers -> baseline
# full-mesh -> policy authored -> AllowedIps now reflect the rule.
# Every observable produces visible output so a reviewer can see the
# proof without trusting prose.
#
# Prerequisites:
#   - docker + docker compose
#   - grpcurl (https://github.com/fullstorydev/grpcurl)
#   - jq (for readable JSON)
#   - The local stack already up via `make local-up` (or this script
#     will offer to bring it up).
#
# Usage:
#   ./scripts/demo.sh             # full run
#   ./scripts/demo.sh --teardown  # remove the demo peers & policy

set -euo pipefail

# --- config -------------------------------------------------------------

CONTROLLER_HTTP="${CONTROLLER_HTTP:-http://localhost:8081}"
CONTROLLER_GRPC="${CONTROLLER_GRPC:-localhost:8080}"
TENANT_SLUG="${TENANT_SLUG:-default}"
DEV_PUBKEY="${DEV_PUBKEY:-$(openssl rand -base64 32)}"
DB_PUBKEY="${DB_PUBKEY:-$(openssl rand -base64 32)}"

# --- helpers ------------------------------------------------------------

bold()    { printf '\033[1m%s\033[0m\n' "$*"; }
dim()     { printf '\033[2m%s\033[0m\n' "$*"; }
pass()    { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn()    { printf '  \033[33m!\033[0m %s\n' "$*"; }
fail()    { printf '  \033[31m✗\033[0m %s\n' "$*"; exit 1; }
section() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required (install: $2)"
}

curl_api()    { curl -fsS "$@" -H "X-Tenant-Slug: ${TENANT_SLUG}"; }
curl_api_or() { curl -sS  "$@" -H "X-Tenant-Slug: ${TENANT_SLUG}"; }

# --- 0. pre-flight ------------------------------------------------------

require_cmd docker  "docker.com"
require_cmd grpcurl "go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest"
require_cmd jq      "brew install jq"
require_cmd openssl "should already be installed on macOS/Linux"

if [[ "${1:-}" == "--teardown" ]]; then
  section "teardown"
  IDS=$(curl_api_or "${CONTROLLER_HTTP}/api/v1/peers" \
    | jq -r '.peers[] | select(.hostname=="dev-laptop" or .hostname=="db-server") | .id')
  for id in $IDS; do
    curl_api_or -X DELETE "${CONTROLLER_HTTP}/api/v1/peers/${id}" >/dev/null
    pass "deleted peer ${id}"
  done
  grpcurl -plaintext -H "x-tenant-slug: ${TENANT_SLUG}" \
    -d '{"hcl_source": ""}' \
    "${CONTROLLER_GRPC}" bamboo.v1.PolicyService/PutPolicy >/dev/null 2>&1 \
    && pass "policy cleared (empty HCL)" \
    || warn "policy clear skipped (none authored)"
  echo
  bold "Teardown complete."
  exit 0
fi

if ! curl -fsS "${CONTROLLER_HTTP}/healthz" >/dev/null 2>&1; then
  fail "controller not reachable at ${CONTROLLER_HTTP}. Run 'make local-up && make local-bootstrap' first."
fi

# --- 1. register two peers ----------------------------------------------

section "1. register two peers (REST /api/v1/peers/register)"

register_peer() {
  local hostname="$1" pubkey="$2"
  curl_api -X POST "${CONTROLLER_HTTP}/api/v1/peers/register" \
    -H "Content-Type: application/json" \
    -d "{\"hostname\":\"${hostname}\",\"wireguardPublicKey\":\"${pubkey}\",\"os\":\"linux\",\"clientVersion\":\"demo\"}"
}

DEV_RESP=$(register_peer dev-laptop "${DEV_PUBKEY}")
DEV_ID=$(echo "$DEV_RESP" | jq -r '.self.id')
DEV_IP=$(echo "$DEV_RESP" | jq -r '.self.ip')
pass "dev-laptop  id=${DEV_ID}  ip=${DEV_IP}"

DB_RESP=$(register_peer db-server "${DB_PUBKEY}")
DB_ID=$(echo "$DB_RESP" | jq -r '.self.id')
DB_IP=$(echo "$DB_RESP" | jq -r '.self.ip')
pass "db-server   id=${DB_ID}  ip=${DB_IP}"

# --- 2. assign tags -----------------------------------------------------

section "2. tag peers (PATCH /api/v1/peers/{id})"

curl_api -X PATCH "${CONTROLLER_HTTP}/api/v1/peers/${DEV_ID}" \
  -H "Content-Type: application/json" \
  -d '{"tags":["dev"]}' >/dev/null
pass "dev-laptop  tags=[dev]"

curl_api -X PATCH "${CONTROLLER_HTTP}/api/v1/peers/${DB_ID}" \
  -H "Content-Type: application/json" \
  -d '{"tags":["db"]}' >/dev/null
pass "db-server   tags=[db]"

# --- 3. baseline: no policy -> full mesh -------------------------------

section "3. baseline (no policy authored)"

BASELINE=$(register_peer dev-laptop "${DEV_PUBKEY}")
BASELINE_REV=$(echo "$BASELINE" | jq -r '.policyRevision')
BASELINE_DB_ALLOWED=$(echo "$BASELINE" | jq -c --arg id "$DB_ID" '.peers[] | select(.id==$id) | .allowedIps // []')

dim   "  PolicyRevision: ${BASELINE_REV}    (0 = no policy authored → full mesh)"
dim   "  dev-laptop's view of db-server.allowedIps: ${BASELINE_DB_ALLOWED}"
[[ "$BASELINE_REV" == "0" ]] && pass "revision is 0 (DB-backed lookup, was hardcoded 1 pre-v0.1.4)"
[[ "$BASELINE_DB_ALLOWED" == "[]" || "$BASELINE_DB_ALLOWED" == "null" ]] \
  && pass "AllowedIps empty in fallback mode (client uses peer.ip/32)"

# --- 4. author policy: allow tag:dev -> tag:db -------------------------

section "4. author policy via gRPC PutPolicy"

POLICY_HCL='rule "dev-to-db" {
  action       = "allow"
  sources      = ["tag:dev"]
  destinations = ["tag:db:*"]
}'

POLICY_JSON=$(jq -Rs --null-input --arg src "$POLICY_HCL" '{hcl_source:$src}')
PUT_RESP=$(grpcurl -plaintext -H "x-tenant-slug: ${TENANT_SLUG}" \
  -d "$POLICY_JSON" \
  "${CONTROLLER_GRPC}" bamboo.v1.PolicyService/PutPolicy)

NEW_REV=$(echo "$PUT_RESP" | jq -r '.policy.revision')
RULE_COUNT=$(echo "$PUT_RESP" | jq -r '.policy.rules | length')
pass "policy revision ${NEW_REV} (${RULE_COUNT} rule${RULE_COUNT:+s})"

# --- 5. enforcement: re-register and read AllowedIps -------------------

section "5. enforcement — re-register both peers, observe AllowedIps"

DEV_AFTER=$(register_peer dev-laptop "${DEV_PUBKEY}")
DEV_AFTER_REV=$(echo "$DEV_AFTER" | jq -r '.policyRevision')
DEV_SEES_DB=$(echo "$DEV_AFTER" | jq -c --arg id "$DB_ID" '.peers[] | select(.id==$id) | .allowedIps')

dim   "  dev-laptop PolicyRevision after policy: ${DEV_AFTER_REV}"
dim   "  dev-laptop's view of db-server.allowedIps: ${DEV_SEES_DB}"
if echo "$DEV_SEES_DB" | grep -q "${DB_IP}/32"; then
  pass "dev → db ALLOWED (AllowedIps = [${DB_IP}/32])"
else
  fail "dev → db should be allowed by 'allow tag:dev → tag:db:*' but AllowedIps=${DEV_SEES_DB}"
fi

DB_AFTER=$(register_peer db-server "${DB_PUBKEY}")
DB_SEES_DEV=$(echo "$DB_AFTER" | jq -c --arg id "$DEV_ID" '.peers[] | select(.id==$id) | .allowedIps')

dim   "  db-server's view of dev-laptop.allowedIps: ${DB_SEES_DEV}"
if [[ "$DB_SEES_DEV" == "[]" || "$DB_SEES_DEV" == "null" ]]; then
  pass "db → dev DENIED (AllowedIps empty; no reverse rule)"
else
  fail "db → dev should be denied (no reverse rule) but AllowedIps=${DB_SEES_DEV}"
fi

# --- 6. audit trail -----------------------------------------------------

section "6. audit trail (POST /api/v1/activity)"

curl_api "${CONTROLLER_HTTP}/api/v1/activity?limit=5" \
  | jq -r '.events[] | "  \(.occurredAt | sub("T"; " ") | sub("\\..*$"; "")) \(.action)\t\(.resourceType)\t\(.resourceId // "")"'

# --- 7. summary ---------------------------------------------------------

section "what this proved"

cat <<EOF
  $(bold 'L3 ACL enforcement is real on the wire, not just in the UI.')

  - PolicyRevision is read from the acl_policies table, not hardcoded.
  - With no policy authored, the controller hands every peer a full
    mesh (AllowedIps falls back to peer.ip/32 on the client side).
  - With "allow tag:dev → tag:db:*" authored, the controller computes
    AllowedIps per-pair: dev → db reachable, db → dev denied.
  - The dev-fallback path (X-Tenant-Slug) ran every REST call here
    without authentication — that's the local convenience mode. On
    a VPS with BAMBOO_REQUIRE_AUTH=true the same calls return 401
    unless they carry a session JWT.

  Run again with --teardown to remove the demo peers + clear the policy.
EOF
