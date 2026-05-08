#!/usr/bin/env bash
# Verify that every hand-written Go source file carries an SPDX license
# identifier. Generated code (proto/gen/**) is excluded.
#
# Exit 0 = all files compliant.
# Exit 1 = at least one file is missing a header.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

EXIT=0
COUNT=0

# Find all *.go files we care about. Bash 3.2 compatible (no mapfile).
while IFS= read -r f; do
  COUNT=$((COUNT + 1))
  # Read first 5 lines, look for SPDX identifier.
  if ! head -n 5 "$f" | grep -q 'SPDX-License-Identifier:'; then
    echo "MISSING SPDX header: $f"
    EXIT=1
  fi
done < <(find . \
  -name '*.go' \
  -not -path './proto/gen/*' \
  -not -path './*/proto/gen/*' \
  -not -path '*/.cache/*' \
  -not -path '*/node_modules/*')

if [[ "$EXIT" -eq 0 ]]; then
  echo "OK: $COUNT files all have SPDX headers."
fi
exit "$EXIT"
