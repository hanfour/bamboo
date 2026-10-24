# SPDX-License-Identifier: AGPL-3.0-or-later
from __future__ import annotations

from datetime import UTC, datetime
from typing import Any

from anomaly.cli import run_all
from anomaly.clickhouse_io import fetch_active_tenants


class _QueryResult:
    def __init__(self, rows: list[tuple[Any, ...]]) -> None:
        self.result_rows = rows


class _FakeClient:
    def __init__(self, rows: list[tuple[Any, ...]]) -> None:
        self.rows = rows
        self.queries: list[tuple[str, dict[str, Any]]] = []

    def query(self, sql: str, parameters: dict[str, Any]) -> _QueryResult:
        self.queries.append((sql, parameters))
        return _QueryResult(self.rows)


def test_fetch_active_tenants_returns_ids_and_passes_window_and_floor():
    client = _FakeClient([("a-tenant",), ("b-tenant",)])
    since = datetime(2026, 10, 1, tzinfo=UTC)

    tenants = fetch_active_tenants(client, since, min_events=50)

    assert tenants == ["a-tenant", "b-tenant"]
    sql, params = client.queries[0]
    assert "connection_events" in sql
    assert "HAVING" in sql.upper()
    assert params == {"since": since, "min_events": 50}


def test_run_all_runs_every_tenant_and_reports_success():
    seen: list[str] = []

    def run_one(tenant: str) -> int:
        seen.append(tenant)
        return 0

    summary = run_all(["t1", "t2"], run_one)

    assert seen == ["t1", "t2"]
    assert summary.ok == ["t1", "t2"]
    assert summary.failed == []
    assert summary.exit_code == 0


def test_run_all_keeps_going_after_one_tenant_raises():
    def run_one(tenant: str) -> int:
        if tenant == "bad":
            raise RuntimeError("clickhouse went away")
        return 0

    summary = run_all(["bad", "good"], run_one)

    assert summary.ok == ["good"]
    assert summary.failed == ["bad"]
    assert summary.exit_code == 1


def test_run_all_counts_a_nonzero_return_as_failure():
    summary = run_all(["t1"], lambda _t: 1)

    assert summary.failed == ["t1"]
    assert summary.exit_code == 1


def test_run_all_with_no_tenants_succeeds_quietly():
    summary = run_all([], lambda _t: 0)

    assert summary.ok == [] and summary.failed == []
    assert summary.exit_code == 0
