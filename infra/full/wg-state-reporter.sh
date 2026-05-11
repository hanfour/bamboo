#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Dump the current WireGuard hub state to a file the controller
# container can read. Run periodically by wg-state-reporter.timer.
#
# Why a file: the controller runs in docker without access to the
# host's WireGuard netlink (network namespace isolation), and giving
# the controller container host-mode networking would also force
# postgres/clickhouse off the docker bridge. Reading a file is
# sufficient — 30-second lag is fine for a UI status column.
#
# Atomic write: rename(2) is atomic on the same filesystem, so the
# controller never observes a half-written dump.

set -euo pipefail

# wg show ... dump needs CAP_NET_ADMIN. The systemd unit runs as
# root; bail loudly when an operator runs the script by hand as
# their normal user (it would otherwise silently produce an empty
# dump, which the reporter would interpret as "no peers" and skip
# the zombie sweep).
if [[ ${EUID} -ne 0 ]]; then
    echo "wg-state-reporter.sh: must run as root (try sudo)" >&2
    exit 1
fi

IFACE="${BAMBOO_WG_IFACE:-bamboo0}"
OUT="${BAMBOO_WG_STATE_PATH:-/var/lib/bamboo/wg-state.txt}"

# Write to a dotfile in the same directory so the rename(2) is
# atomic (same filesystem) and any directory listing tools hide
# the in-progress write.
TMP="$(dirname "$OUT")/.$(basename "$OUT").tmp"

# When the interface doesn't exist yet (controller-only deploy
# before wg-quick is configured) write an empty file. The
# reporter treats an empty dump as "skip the zombie sweep" so
# this won't mass-flip peers offline.
if ip link show "$IFACE" >/dev/null 2>&1; then
    wg show "$IFACE" dump > "$TMP"
else
    : > "$TMP"
fi

mv -f "$TMP" "$OUT"
