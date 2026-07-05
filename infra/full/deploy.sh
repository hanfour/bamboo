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

# --- 1. Pre-deploy backup ----------------------------------------------
# A failed migration is the scariest step; take a fresh verified dump
# first. Reuse backup.sh so the dump is integrity-checked + retained the
# same way the nightly timer's are.
if [ -x ./backup.sh ]; then
    log "taking a pre-deploy backup"
    ./backup.sh
else
    log "backup.sh not found/executable — skipping pre-deploy backup (NOT recommended)"
fi

# --- 2. Pull the new app images ----------------------------------------
log "pulling images (controller, web, relay)"
docker compose pull controller web relay

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
