# SPDX-License-Identifier: AGPL-3.0-or-later
"""ClickHouse writer for anomaly_findings.

The Go controller treats the table as read-only; this is the producer
side. Schemas are owned by the controller (`apps/controller/internal/
clickhouse/clickhouse.go applySchema`) so the table is created on
controller boot — never here.
"""

from __future__ import annotations

import json
import uuid
from datetime import UTC, datetime
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from clickhouse_connect.driver.client import Client

# UUID 0000... acts as a "no event reference" sentinel. Storing
# it explicitly keeps the column NOT NULL semantics intact in CH.
_NIL_UUID = uuid.UUID(int=0)

ANOMALY_COLUMNS: list[str] = [
    "id",
    "tenant_id",
    "occurred_at",
    "generated_at",
    "score",
    "model_version",
    "event_id",
    "event_summary",
]


def write_findings(
    client: Client,
    tenant_id: str,
    findings: list[dict[str, Any]],
    *,
    model_version: str,
) -> int:
    """Insert findings into anomaly_findings.

    Each `findings` entry is expected to carry:
      - occurred_at  datetime
      - score        float
      - event        dict (the connection_event we scored)
      - event_id     uuid string, optional

    Returns the number of rows written. No-op for an empty list.
    """

    if not findings:
        return 0

    tenant_uuid = uuid.UUID(tenant_id)
    generated_at = datetime.now(UTC)

    rows: list[tuple[Any, ...]] = []
    for f in findings:
        event_id = f.get("event_id")
        try:
            event_uuid = uuid.UUID(event_id) if event_id else _NIL_UUID
        except ValueError:
            event_uuid = _NIL_UUID
        rows.append(
            (
                uuid.uuid4(),
                tenant_uuid,
                f["occurred_at"],
                generated_at,
                float(f["score"]),
                model_version,
                event_uuid,
                json.dumps(_summarise(f.get("event") or {}), default=str),
            )
        )

    client.insert("anomaly_findings", rows, column_names=ANOMALY_COLUMNS)
    return len(rows)


# Fields the controller renders verbatim in the recommendation evidence.
# Keep this stable; downstream code may depend on the JSON shape.
_SUMMARY_KEYS = (
    "event_type",
    "path",
    "bytes_sent",
    "bytes_received",
    "rtt_ms",
    "occurred_at",
)


def _summarise(event: dict[str, Any]) -> dict[str, Any]:
    return {k: event.get(k) for k in _SUMMARY_KEYS if k in event}
