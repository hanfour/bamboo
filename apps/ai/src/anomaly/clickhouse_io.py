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


def fetch_active_tenants(
    client: Client,
    since: datetime,
    *,
    min_events: int,
) -> list[str]:
    """Tenants with at least `min_events` connection events since `since`.

    A scheduled run trains every tenant this returns. The floor keeps a
    tenant with a handful of events from getting a model fitted to noise.
    ClickHouse is the source because it is where the training data lives;
    a tenant with no events has nothing to train on anyway.
    """

    rows = client.query(
        """
        SELECT toString(tenant_id) AS tenant
        FROM connection_events
        WHERE occurred_at >= %(since)s
        GROUP BY tenant_id
        HAVING count() >= %(min_events)s
        ORDER BY tenant
        """,
        parameters={"since": since, "min_events": min_events},
    ).result_rows
    return [tenant for (tenant,) in rows]
