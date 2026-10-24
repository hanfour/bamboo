# SPDX-License-Identifier: AGPL-3.0-or-later
"""End-to-end check of `bamboo-ai run-all` against a real ClickHouse.

Skipped unless CLICKHOUSE_URL_TEST is set (CI sets it to a service
container). Each test gets its own database, so it never touches data
the controller wrote.

The two CREATE TABLE statements mirror apps/controller/internal/clickhouse
/clickhouse.go, which owns the schema. Keep the columns in step with it.
"""

from __future__ import annotations

import os
import random
import uuid
from datetime import UTC, datetime, timedelta

import pytest

from anomaly.cli import _parse_url, main

URL = os.environ.get("CLICKHOUSE_URL_TEST")
pytestmark = pytest.mark.skipif(not URL, reason="CLICKHOUSE_URL_TEST not set")

CONNECTION_EVENTS = """
CREATE TABLE connection_events (
    id                   UUID,
    tenant_id            UUID,
    occurred_at          DateTime64(3),
    source_peer_id       UUID,
    destination_peer_id  UUID,
    event_type           LowCardinality(String),
    path                 LowCardinality(String),
    prev_path            LowCardinality(String),
    bytes_sent           UInt64,
    bytes_received       UInt64,
    rtt_ms               UInt32,
    reason               String,
    matched_rule_id      String
) ENGINE = MergeTree ORDER BY (tenant_id, occurred_at)
"""

ANOMALY_FINDINGS = """
CREATE TABLE anomaly_findings (
    id              UUID,
    tenant_id       UUID,
    occurred_at     DateTime64(3),
    generated_at    DateTime64(3),
    score           Float32,
    model_version   LowCardinality(String),
    event_id        UUID,
    event_summary   String
) ENGINE = MergeTree ORDER BY (tenant_id, generated_at)
"""


@pytest.fixture
def ch_db():
    from clickhouse_connect import get_client

    base = _parse_url(URL)
    name = f"ai_test_{uuid.uuid4().hex[:12]}"
    admin = get_client(**base)
    admin.command(f"CREATE DATABASE {name}")
    db = get_client(**{**base, "database": name})
    db.command(CONNECTION_EVENTS)
    db.command(ANOMALY_FINDINGS)
    parts = URL.rsplit("/", 1)
    try:
        yield db, f"{parts[0]}/{name}"
    finally:
        db.close()
        admin.command(f"DROP DATABASE IF EXISTS {name}")
        admin.close()


def _events(tenant: uuid.UUID, n: int, *, outliers: int = 0) -> list[tuple]:
    rnd = random.Random(7)
    now = datetime.now(UTC)
    rows = []
    for i in range(n):
        spike = i < outliers
        rows.append(
            (
                uuid.uuid4(),
                tenant,
                now - timedelta(minutes=5 * i + 1),
                uuid.uuid4(),
                uuid.uuid4(),
                "CONNECTION_ESTABLISHED",
                "RELAY" if spike else "DIRECT_HOST",
                "",
                (500_000_000 if spike else rnd.randint(900, 1100)),
                (900_000_000 if spike else rnd.randint(1900, 2100)),
                (2_000 if spike else rnd.randint(18, 30)),
                "",
                "",
            )
        )
    return rows


COLS = [
    "id", "tenant_id", "occurred_at", "source_peer_id", "destination_peer_id",
    "event_type", "path", "prev_path", "bytes_sent", "bytes_received",
    "rtt_ms", "reason", "matched_rule_id",
]  # fmt: skip


def test_run_all_trains_busy_tenants_and_skips_thin_ones(ch_db, tmp_path, monkeypatch):
    db, url = ch_db
    busy, thin = uuid.uuid4(), uuid.uuid4()
    db.insert("connection_events", _events(busy, 300, outliers=5), column_names=COLS)
    db.insert("connection_events", _events(thin, 5), column_names=COLS)
    monkeypatch.setenv("BAMBOO_AI_MODEL_DIR", str(tmp_path))

    code = main(["run-all", "--clickhouse-url", url, "--since", "7d", "--min-events", "50"])

    assert code == 0
    tenants = {
        str(t)
        for (t,) in db.query("SELECT DISTINCT tenant_id FROM anomaly_findings").result_rows
    }
    assert tenants == {str(busy)}
    assert (tmp_path / f"{busy}.joblib").exists()
    assert not (tmp_path / f"{thin}.joblib").exists()
