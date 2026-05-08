# SPDX-License-Identifier: AGPL-3.0-or-later
"""ClickHouse data loader for connection_events.

This module is intentionally thin. It returns dicts the rest of the
pipeline can feed through `extract_features`; it does not own the
DataFrame conversion.
"""

from __future__ import annotations

from datetime import datetime
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from clickhouse_connect.driver.client import Client


def fetch_connection_events(
    client: Client,
    tenant_id: str,
    since: datetime,
    *,
    limit: int = 100_000,
) -> list[dict[str, Any]]:
    """Read connection_events for a tenant since `since`.

    `client` is a clickhouse-connect Client. Caller owns lifecycle.
    Returns a list of dicts with keys matching extract_features
    expectations.
    """

    rows = client.query(
        """
        SELECT
            occurred_at,
            event_type,
            path,
            bytes_sent,
            bytes_received,
            rtt_ms
        FROM connection_events
        WHERE tenant_id = %(tenant_id)s
          AND occurred_at >= %(since)s
        ORDER BY occurred_at ASC
        LIMIT %(limit)s
        """,
        parameters={"tenant_id": tenant_id, "since": since, "limit": limit},
    ).result_rows

    return [
        {
            "occurred_at": occurred_at,
            "event_type": event_type,
            "path": path,
            "bytes_sent": bytes_sent,
            "bytes_received": bytes_received,
            "rtt_ms": rtt_ms,
        }
        for (occurred_at, event_type, path, bytes_sent, bytes_received, rtt_ms) in rows
    ]
