#!/usr/bin/env bash
# Audit a NetBird tree against the import inventory.
#
# Two reports:
#   1. Brand-string hits (count by directory). Expected on upstream;
#      should drop to zero after import + scrub.
#   2. Top-level directories that exist upstream but are not mentioned in
#      our inventory (potential drift).

set -euo pipefail

TARGET="${1:-$HOME/scratch/bamboo-fork/netbird-upstream}"
INVENTORY="${INVENTORY:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/docs/development/netbird-import-inventory.md}"

if [ ! -d "$TARGET" ]; then
    echo "error: $TARGET does not exist. Run scripts/netbird-prep.sh first." >&2
    exit 1
fi
if [ ! -f "$INVENTORY" ]; then
    echo "error: $INVENTORY not found." >&2
    exit 1
fi

echo "==> Brand string audit on $TARGET"
PATTERNS='(netbird|wiretrustee|wt-)'

TOTAL_HITS=$(find "$TARGET" -type f \
    -not -path '*/.git/*' \
    -not -path '*/node_modules/*' \
    -not -path '*/vendor/*' \
    -exec grep -liE "$PATTERNS" {} \; 2>/dev/null | wc -l | tr -d ' ')

echo "  $TOTAL_HITS files contain NetBird-related identifiers."
echo "  (high count is expected on an unmodified upstream tree)"

echo ""
echo "==> Inventory drift check"

cd "$TARGET"
ACTUAL_DIRS=$(find . -maxdepth 1 -mindepth 1 -type d \
    -not -name '.git' \
    -not -name 'node_modules' \
    -not -name 'vendor' \
    -printf '%P\n' 2>/dev/null \
    || find . -maxdepth 1 -mindepth 1 -type d \
       -not -name '.git' -not -name 'node_modules' -not -name 'vendor' \
       | sed 's|^\./||')

UNDOCUMENTED=()
while IFS= read -r d; do
    [ -z "$d" ] && continue
    # Look for the directory name (with trailing slash) in the inventory's
    # path column. Skip entries already documented.
    if ! grep -qE "\`$d/" "$INVENTORY"; then
        UNDOCUMENTED+=("$d")
    fi
done <<< "$ACTUAL_DIRS"

if [ ${#UNDOCUMENTED[@]} -eq 0 ]; then
    echo "  OK: every top-level directory upstream has an inventory entry."
else
    echo "  Top-level directories present upstream but missing from inventory:"
    for d in "${UNDOCUMENTED[@]}"; do
        echo "    - $d"
    done
    echo ""
    echo "  Update $INVENTORY before importing."
fi
