# SPDX-License-Identifier: AGPL-3.0-or-later
from __future__ import annotations

import json
import uuid
from datetime import UTC, datetime
from typing import Any

from anomaly.writer import ANOMALY_COLUMNS, write_findings


class _FakeClient:
    """Captures inserts so tests can inspect the wire shape without a CH."""

    def __init__(self) -> None:
        self.calls: list[tuple[str, list[tuple[Any, ...]], list[str]]] = []

    def insert(self, table: str, rows: list[tuple[Any, ...]], column_names: list[str]) -> None:
        self.calls.append((table, rows, column_names))


def _finding(score: float = 0.7, **event_fields):
    base_event = {
        "event_type": "CONNECTION_ESTABLISHED",
        "path": "DIRECT_HOST",
        "bytes_sent": 1024,
        "bytes_received": 2048,
        "rtt_ms": 25,
    }
    base_event.update(event_fields)
    return {
        "occurred_at": datetime(2026, 5, 7, 12, 0, 0, tzinfo=UTC),
        "score": score,
        "event": base_event,
    }


def test_write_findings_inserts_into_correct_table_with_canonical_columns():
    client = _FakeClient()
    tenant_id = str(uuid.uuid4())
    written = write_findings(
        client, tenant_id, [_finding()], model_version="iso-forest-v1"
    )
    assert written == 1
    assert len(client.calls) == 1
    table, rows, columns = client.calls[0]
    assert table == "anomaly_findings"
    assert columns == ANOMALY_COLUMNS
    assert len(rows[0]) == len(ANOMALY_COLUMNS)


def test_write_findings_serialises_event_summary_as_compact_json():
    client = _FakeClient()
    write_findings(
        client,
        str(uuid.uuid4()),
        [_finding(bytes_sent=99_999)],
        model_version="iso-forest-v1",
    )
    summary = client.calls[0][1][0][7]
    decoded = json.loads(summary)
    assert decoded["bytes_sent"] == 99_999
    assert decoded["event_type"] == "CONNECTION_ESTABLISHED"


def test_write_findings_falls_back_to_nil_uuid_when_event_id_missing():
    client = _FakeClient()
    write_findings(
        client, str(uuid.uuid4()), [_finding()], model_version="iso-forest-v1"
    )
    event_uuid = client.calls[0][1][0][6]
    assert event_uuid == uuid.UUID(int=0)


def test_write_findings_handles_invalid_event_id_gracefully():
    client = _FakeClient()
    finding = _finding()
    finding["event_id"] = "not-a-uuid"
    write_findings(client, str(uuid.uuid4()), [finding], model_version="iso-forest-v1")
    event_uuid = client.calls[0][1][0][6]
    assert event_uuid == uuid.UUID(int=0)


def test_write_findings_empty_list_is_a_noop():
    client = _FakeClient()
    written = write_findings(client, str(uuid.uuid4()), [], model_version="iso-forest-v1")
    assert written == 0
    assert client.calls == []
