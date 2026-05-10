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

IFACE="${BAMBOO_WG_IFACE:-bamboo0}"
OUT="${BAMBOO_WG_STATE_PATH:-/var/lib/bamboo/wg-state.txt}"
TMP="${OUT}.tmp"

# wg show ... dump needs CAP_NET_ADMIN; the systemd unit runs as root.
# When the interface doesn't exist yet (e.g. controller-only deploy
# before wg-quick is configured) write an empty file so the reporter's
# parser produces an empty peer list and marks any DB peer offline.
if ip link show "$IFACE" >/dev/null 2>&1; then
    wg show "$IFACE" dump > "$TMP"
else
    : > "$TMP"
fi

mv -f "$TMP" "$OUT"
