#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# infra/full/deploy.sh — one safe, repeatable deploy for the single-VPS
# stack. Codifies the manual upgrade sequence from single-vps.md so a
# deploy can't skip the migration step or leave `web` on an old image
# (both real failure modes — the prod web UI has drifted behind the
# controller before precisely because deploys were hand-typed).
#
# Usage:
#   ./deploy.sh              # deploy the tag currently pinned in .env
#   ./deploy.sh v0.1.15      # pin BAMBOO_IMAGE_TAG=v0.1.15 in .env, then deploy
#
# Steps: (0) pin tag → (1) fresh verified DB backup → (2) pull images →
# (3) migrate status + up → (4) recreate controller+web+relay → (5) ps +
# health. Additive migrations (the only kind shipped) are safe to run
# while the old controller is still up, so downtime is just the recreate.
set -euo pipefail

cd "$(dirname "$0")"

if [ ! -f .env ]; then
    echo "ERROR: .env is missing. Copy .env.example to .env and fill it in." >&2
    exit 2
fi

log() { printf '\n==> %s\n' "$*"; }

# --- 0. Pin the requested image tag ------------------------------------
TAG="${1:-}"
if [ -n "$TAG" ]; then
    if grep -q '^BAMBOO_IMAGE_TAG=' .env; then
        # Portable in-place edit (GNU + BSD sed differ on -i).
        tmp="$(mktemp)"
        sed "s/^BAMBOO_IMAGE_TAG=.*/BAMBOO_IMAGE_TAG=${TAG}/" .env > "$tmp" && mv "$tmp" .env
    else
        printf 'BAMBOO_IMAGE_TAG=%s\n' "$TAG" >> .env
    fi
    log "pinned BAMBOO_IMAGE_TAG=${TAG} in .env"
fi

TARGET="$(grep '^BAMBOO_IMAGE_TAG=' .env | tail -n1 | cut -d= -f2-)"
log "deploying tag: ${TARGET:-latest (BAMBOO_IMAGE_TAG unset)}"

# --- 0b. Host port preflight -------------------------------------------
# The controller publishes 127.0.0.1:${BAMBOO_CONTROLLER_HOST_PORT:-8081}
# for the host-network ingress, and the ingress binds 127.0.0.1:8091 and
# 10.213.0.1:8090. If another process holds one of them, the recreate
# below would leave the control plane (or the ingress) unable to start.
# Check first and abort before anything touches the running stack.
env_value() { grep "^$1=" .env | tail -n1 | cut -d= -f2- || true; }

# listening ADDR PORT: true when something listens on ADDR:PORT or on a
# wildcard address for PORT (a wildcard bind blocks the specific one).
listening() {
    ss -ltnH 2>/dev/null | awk '{print $4}' | grep -Eqx "(${1//./\\.}|0\.0\.0\.0|\*|\[::\]):$2"
}

# require_port ADDR PORT OWNER_CHECK...: abort unless ADDR:PORT is free or
# OWNER_CHECK (a command) confirms our own container holds it.
require_port() {
    local addr="$1" port="$2"
    shift 2
    if ! listening "$addr" "$port"; then
        return 0
    fi
    if "$@"; then
        return 0
    fi
    echo "ERROR: ${addr}:${port} is already in use by a process outside this stack." >&2
    echo "       Nothing was changed; the running controller is untouched." >&2
    echo "       Find it with: sudo ss -ltnp 'sport = :${port}'" >&2
    if [ "$port" = "$CONTROLLER_HOST_PORT" ]; then
        echo "       Stop it, or set BAMBOO_CONTROLLER_HOST_PORT to a free port in .env." >&2
    else
        echo "       Stop it before enabling the ingress profile." >&2
    fi
    exit 3
}

# Our controller publishes the port: docker compose port prints it.
controller_holds_port() {
    docker compose port controller 8081 2>/dev/null | grep -qx "127.0.0.1:${CONTROLLER_HOST_PORT}"
}

# The ingress uses the host network, so docker cannot attribute the
# socket; a running ingress container of this project is taken as the
# holder (recreating it releases the port first).
ingress_running() {
    docker compose ps --status running --services 2>/dev/null | grep -qx ingress
}

CONTROLLER_HOST_PORT="$(env_value BAMBOO_CONTROLLER_HOST_PORT)"
CONTROLLER_HOST_PORT="${CONTROLLER_HOST_PORT:-8081}"
INGRESS_ENABLED=0
if grep -Eq '^COMPOSE_PROFILES=.*\bingress\b' .env; then
    INGRESS_ENABLED=1
fi
if command -v ss >/dev/null 2>&1; then
    log "checking host ports"
    require_port 127.0.0.1 "$CONTROLLER_HOST_PORT" controller_holds_port
    if [ "$INGRESS_ENABLED" = 1 ]; then
        require_port 127.0.0.1 8091 ingress_running
        require_port 10.213.0.1 8090 ingress_running
    fi
else
    log "ss not found; skipping the host port preflight (check 127.0.0.1:${CONTROLLER_HOST_PORT} by hand)"
fi

# --- 1. Pre-deploy backup ----------------------------------------------
# A failed migration is the scariest step; take a fresh verified dump
# first. Reuse backup.sh so the dump is integrity-checked + retained the
# same way the nightly timer's are.
if [ -x ./backup.sh ]; then
    log "taking a pre-deploy backup"
    # The nightly timer's backup dir is root-only (0700), so a deploy run
    # as a normal user cannot write there. Use sudo in that case instead of
    # skipping the backup. sudo keeps the dumps in one place with the same
    # ownership. A BACKUP_DIR the caller can write to is used as is.
    if [ "$(id -u)" -ne 0 ] && [ ! -w "${BACKUP_DIR:-/var/backups/bamboo}" ]; then
        sudo ./backup.sh
    else
        ./backup.sh
    fi
else
    log "backup.sh not found/executable — skipping pre-deploy backup (NOT recommended)"
fi

# --- 2. Pull the new app images ----------------------------------------
log "pulling images (controller, web, relay)"
docker compose pull controller web relay
# The nightly AI job's image. Tags published before the ai image existed
# have none, so a miss only logs: the timer keeps the image it has.
docker compose --profile jobs pull ai \
    || log "no bamboo-ai image for ${BAMBOO_IMAGE_TAG:-latest}; the nightly AI job keeps its current image"

# --- 3. Migrations ------------------------------------------------------
# serve does NOT auto-migrate; run it explicitly between pull and
# recreate. Show what's pending first for the operator's log.
log "migration status"
docker compose run --rm controller migrate status
log "applying migrations"
docker compose run --rm controller migrate up

# --- 4. Recreate the app services --------------------------------------
# All three together so web never lags the controller.
log "recreating controller + web + relay"
docker compose up -d controller web relay

# Public services ingress is opt-in (COMPOSE_PROFILES=ingress in .env).
# `up -d ingress` also applies a changed caddy service definition, since
# the ingress depends on Caddy's ingress_edge bridge.
if [ "$INGRESS_ENABLED" = 1 ]; then
    log "pulling + recreating ingress"
    docker compose pull ingress && docker compose up -d ingress \
        || echo "warning: ingress did not start; check 'docker compose ps caddy ingress' and 'docker compose logs ingress'"
fi

# The Caddyfile is a bind mount, so a changed file does not restart Caddy.
# Reload it in place so route changes (for example the gRPC path) apply.
if docker compose ps --status running --services 2>/dev/null | grep -qx caddy; then
    docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile \
        || echo "warning: caddy reload failed; check the Caddyfile with 'docker compose exec caddy caddy validate --config /etc/caddy/Caddyfile'"
fi

# --- 5. Verify ----------------------------------------------------------
log "service status"
docker compose ps
DOMAIN="$(grep '^DOMAIN=' .env | tail -n1 | cut -d= -f2-)"
if [ -n "${DOMAIN:-}" ]; then
    log "health check https://${DOMAIN}/healthz"
    for _ in $(seq 1 30); do
        if curl -sfk "https://${DOMAIN}/healthz" >/dev/null 2>&1; then
            echo "OK — controller healthy"
            break
        fi
        sleep 2
    done
fi

log "deploy complete (tag: ${TARGET:-latest})"
echo "If the controller is unhealthy, roll back: set the previous tag in .env,"
echo "run 'docker compose run --rm controller migrate down' if you migrated,"
echo "then re-run ./deploy.sh."
