#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# infra/full/backup.sh — Postgres backup for the single-VPS deploy.
#
# Dumps the bamboo database, keeps a rotated set of local copies, and
# (optionally) uploads each dump to S3-compatible storage for off-box
# durability. The single-VPS box is a single point of failure: the
# Postgres volume holds every tenant / user / peer / preauth-key / ACL /
# audit row and previously had NO automated backup. This closes that gap.
#
# Idempotent + safe to run by hand or from the bamboo-backup systemd
# timer. Every knob has a sane default, so `./backup.sh` from infra/full
# just works (local backups). Set BACKUP_S3_URI for off-box copies.
#
# Config (env, or lines in .env):
#   BACKUP_DIR         local backup dir            (default /var/backups/bamboo)
#   BACKUP_KEEP        local copies to retain      (default 14)
#   BACKUP_S3_URI      s3://bucket/prefix          (default: off)
#   BACKUP_S3_ENDPOINT S3-compatible endpoint URL  (R2/MinIO; default: AWS)
#   BAMBOO_COMPOSE_DIR compose project dir         (default: this script's dir)
#
# NOTE: ClickHouse is intentionally out of scope — it stores best-effort
# telemetry (traces / connection events) that is reconstructable and not
# authoritative. Only Postgres holds durable control-plane state.
set -euo pipefail

# Resolve the compose project dir: explicit env wins (the systemd unit
# sets it), else the directory this script lives in — which is correct
# when an operator runs `./backup.sh` from infra/full.
COMPOSE_DIR="${BAMBOO_COMPOSE_DIR:-$(cd "$(dirname "$0")" && pwd)}"
cd "$COMPOSE_DIR"

# Load .env (if present) so BACKUP_* / POSTGRES_* set there are honored.
# docker compose reads it too; we source it for the shell-side knobs.
if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    . ./.env
    set +a
fi

BACKUP_DIR="${BACKUP_DIR:-/var/backups/bamboo}"
BACKUP_KEEP="${BACKUP_KEEP:-14}"
PG_SERVICE="${BACKUP_PG_SERVICE:-postgres}"
PG_USER="${POSTGRES_USER:-bamboo}"
PG_DB="${POSTGRES_DB:-bamboo}"
S3_URI="${BACKUP_S3_URI:-}"
S3_ENDPOINT="${BACKUP_S3_ENDPOINT:-}"

log() { printf '==> %s\n' "$*"; }
err() { printf 'ERROR: %s\n' "$*" >&2; }

mkdir -p "$BACKUP_DIR"

# Single-instance lock so a slow dump can't overlap the next timer fire.
exec 9>"${BACKUP_DIR}/.backup.lock"
if command -v flock >/dev/null 2>&1; then
    flock -n 9 || { err "another backup is already running; skipping"; exit 0; }
fi

ts="$(date -u +%Y%m%d-%H%M%S)"
out="${BACKUP_DIR}/bamboo-${ts}.sql.gz"
tmp="${out}.partial"

# Dump to a temp file first; only promote to the timestamped name after
# integrity checks pass, so a failed dump never masquerades as a good
# backup (nor triggers rotation that would delete real ones).
log "dumping ${PG_DB} from the ${PG_SERVICE} container"
if ! docker compose exec -T "$PG_SERVICE" pg_dump -U "$PG_USER" "$PG_DB" | gzip > "$tmp"; then
    err "pg_dump failed"
    rm -f "$tmp"
    exit 1
fi
if ! gzip -t "$tmp" 2>/dev/null; then
    err "dump failed gzip integrity check"
    rm -f "$tmp"
    exit 1
fi
size="$(wc -c < "$tmp")"
if [ "$size" -lt 1024 ]; then
    err "dump is suspiciously small (${size} bytes); refusing to keep it"
    rm -f "$tmp"
    exit 1
fi
mv "$tmp" "$out"
log "wrote ${out} ($(du -h "$out" | cut -f1))"

# Off-box upload (optional). S3 lifecycle rules should own off-box
# retention; local rotation below only prunes the on-box copies.
if [ -n "$S3_URI" ]; then
    if command -v aws >/dev/null 2>&1; then
        endpoint_args=()
        [ -n "$S3_ENDPOINT" ] && endpoint_args=(--endpoint-url "$S3_ENDPOINT")
        if aws "${endpoint_args[@]}" s3 cp "$out" "${S3_URI%/}/$(basename "$out")"; then
            log "uploaded to ${S3_URI%/}/$(basename "$out")"
        else
            err "off-box upload failed (local copy kept)"
        fi
    else
        err "BACKUP_S3_URI is set but the aws CLI is not installed; kept local copy only"
    fi
fi

# Rotate local copies: keep the newest $BACKUP_KEEP, delete older.
while IFS= read -r f; do
    [ -n "$f" ] || continue
    rm -f "$f" && log "rotated out $(basename "$f")"
done < <(ls -1t "${BACKUP_DIR}"/bamboo-*.sql.gz 2>/dev/null | tail -n +"$((BACKUP_KEEP + 1))")

log "backup complete"
