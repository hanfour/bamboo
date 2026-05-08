#!/usr/bin/env bash
# Prepare a NetBird upstream sandbox per docs/development/netbird-fork-sop.md.
#
# Creates two clones under $SANDBOX_ROOT (default ~/scratch/bamboo-fork):
#   netbird-upstream   read-only reference; never modify
#   netbird-working    work area; remote 'upstream' = netbirdio/netbird
#
# Records the upstream HEAD SHA in $SANDBOX_ROOT/UPSTREAM_SHA so subsequent
# import PRs can cite a precise baseline.
#
# Idempotent: re-running fetches the latest main without destroying state.

set -euo pipefail

SANDBOX_ROOT="${SANDBOX_ROOT:-$HOME/scratch/bamboo-fork}"
UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/netbirdio/netbird.git}"

mkdir -p "$SANDBOX_ROOT"
cd "$SANDBOX_ROOT"

if [ -d netbird-upstream/.git ]; then
    echo "==> updating netbird-upstream"
    git -C netbird-upstream fetch --quiet origin
    git -C netbird-upstream checkout --quiet origin/HEAD 2>/dev/null \
        || git -C netbird-upstream pull --quiet origin
else
    echo "==> cloning netbird-upstream from $UPSTREAM_URL"
    git clone --quiet "$UPSTREAM_URL" netbird-upstream
fi

if [ -d netbird-working/.git ]; then
    echo "==> updating netbird-working"
    git -C netbird-working fetch --quiet upstream 2>/dev/null || true
else
    echo "==> cloning netbird-working from $UPSTREAM_URL"
    git clone --quiet "$UPSTREAM_URL" netbird-working
    git -C netbird-working remote rename origin upstream
fi

SHA="$(git -C netbird-upstream rev-parse HEAD)"
echo "$SHA" > "$SANDBOX_ROOT/UPSTREAM_SHA"

cat <<EOF

Sandbox ready at $SANDBOX_ROOT
  netbird-upstream  (read-only reference; do not modify)
  netbird-working   (work area; remote 'upstream' = netbirdio/netbird)
  UPSTREAM_SHA      = $SHA

Next: run scripts/netbird-audit.sh "$SANDBOX_ROOT/netbird-upstream"
to see how the actual top-level layout compares with our inventory at
docs/development/netbird-import-inventory.md.
EOF
