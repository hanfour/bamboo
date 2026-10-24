# SPDX-License-Identifier: AGPL-3.0-or-later
"""Command-line entry point for the anomaly model.

Subcommands:

  bamboo-ai train --tenant <id> [--since 30d] [--out PATH]
      Loads connection_events for the tenant from ClickHouse, fits an
      Isolation Forest, and saves the joblib bundle.

  bamboo-ai score --tenant <id> --model PATH [--limit 10]
      Loads recent events, scores them, prints the top anomalies as
      JSON for the controller / operator to ingest.

  bamboo-ai run --tenant <id>
      Trains one tenant and writes findings to anomaly_findings.

  bamboo-ai run-all [--since 7d] [--min-events 50]
      Runs `run` for every tenant with enough recent events. This is
      what the nightly job calls (see docs/adr/0015-ai-tier2-scheduled-run.md).
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from pathlib import Path

from anomaly.features import extract_features
from anomaly.model import load_model, save_model, score, train


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="bamboo-ai")
    sub = parser.add_subparsers(dest="cmd", required=True)

    train_p = sub.add_parser("train", help="train a model from ClickHouse events")
    _add_common(train_p)
    train_p.add_argument("--out", type=Path, required=True, help="model output path")
    train_p.add_argument("--contamination", type=float, default=0.05)
    train_p.set_defaults(func=cmd_train)

    score_p = sub.add_parser("score", help="score recent events with a saved model")
    _add_common(score_p)
    score_p.add_argument("--model", type=Path, required=True)
    score_p.add_argument("--limit", type=int, default=10, help="top-K most anomalous to print")
    score_p.set_defaults(func=cmd_score)

    write_p = sub.add_parser(
        "score-and-write",
        help="score recent events and write findings above threshold to ClickHouse",
    )
    _add_common(write_p)
    write_p.add_argument("--model", type=Path, required=True)
    write_p.add_argument("--threshold", type=float, default=0.6, help="min score to record")
    write_p.add_argument("--limit", type=int, default=20, help="cap on findings per run")
    write_p.add_argument(
        "--model-version",
        default="isolation-forest-v1",
        help="label stored alongside each finding so the controller can show provenance",
    )
    write_p.set_defaults(func=cmd_score_and_write)

    run_p = sub.add_parser(
        "run",
        help="train a model for one tenant, then write findings at or above the threshold",
    )
    _add_common(run_p)
    run_p.add_argument(
        "--out",
        type=Path,
        default=None,
        help="model path (default: $BAMBOO_AI_MODEL_DIR/<tenant>.joblib)",
    )
    run_p.add_argument("--contamination", type=float, default=0.05)
    run_p.add_argument("--threshold", type=float, default=0.6, help="min score to record")
    run_p.add_argument("--limit", type=int, default=20, help="cap on findings per run")
    run_p.add_argument("--model-version", default="isolation-forest-v1")
    run_p.set_defaults(func=cmd_run)

    all_p = sub.add_parser(
        "run-all",
        help="run `run` for every tenant with enough events in the window",
    )
    all_p.add_argument("--since", default="7d", help="lookback window (default 7d)")
    all_p.add_argument(
        "--min-events",
        type=int,
        default=50,
        help="skip tenants with fewer events than this in the window",
    )
    all_p.add_argument(
        "--clickhouse-url",
        default=os.environ.get(
            "CLICKHOUSE_URL", "http://bamboo:dev@localhost:18123/bamboo"
        ),
    )
    all_p.add_argument("--contamination", type=float, default=0.05)
    all_p.add_argument("--threshold", type=float, default=0.6, help="min score to record")
    all_p.add_argument("--limit", type=int, default=20, help="cap on findings per tenant")
    all_p.add_argument("--model-version", default="isolation-forest-v1")
    all_p.set_defaults(func=cmd_run_all)

    args = parser.parse_args(argv)
    return args.func(args)


def _add_common(p: argparse.ArgumentParser) -> None:
    p.add_argument("--tenant", required=True, help="tenant UUID")
    p.add_argument(
        "--since",
        default="30d",
        help="lookback window, e.g. 30d / 24h / 7d (default 30d)",
    )
    p.add_argument(
        "--clickhouse-url",
        default=os.environ.get(
            "CLICKHOUSE_URL", "http://bamboo:dev@localhost:18123/bamboo"
        ),
    )


def cmd_run(args: argparse.Namespace) -> int:
    """One tenant, one pass: fit the model, then persist findings.

    The controller reads anomaly_findings from the last 24 hours and
    turns rows at score >= 0.6 into KIND_FLAG_ANOMALOUS recommendations.
    Schedule this command per tenant; it does not stay running.
    """
    if args.out is None:
        model_dir = Path(os.environ.get("BAMBOO_AI_MODEL_DIR", "models"))
        args.out = model_dir / f"{args.tenant}.joblib"
    args.model = args.out
    trained = cmd_train(args)
    if trained != 0:
        return trained
    return cmd_score_and_write(args)


@dataclass
class RunAllSummary:
    ok: list[str] = field(default_factory=list)
    failed: list[str] = field(default_factory=list)

    @property
    def exit_code(self) -> int:
        return 1 if self.failed else 0


def run_all(tenants: list[str], run_one: Callable[[str], int]) -> RunAllSummary:
    """Run each tenant in turn. One tenant failing does not stop the rest."""
    summary = RunAllSummary()
    for tenant in tenants:
        try:
            code = run_one(tenant)
        except Exception as e:  # noqa: BLE001 - isolate one tenant's failure
            print(f"tenant {tenant}: {type(e).__name__}: {e}", file=sys.stderr)
            code = 1
        (summary.ok if code == 0 else summary.failed).append(tenant)
    return summary


def cmd_run_all(args: argparse.Namespace) -> int:
    """Nightly entry point: discover tenants, then train and score each."""
    from clickhouse_connect import get_client  # noqa: PLC0415 - intentional lazy import

    from anomaly.clickhouse_io import fetch_active_tenants

    since = datetime.now(UTC) - _parse_window(args.since)
    client = get_client(**_parse_url(args.clickhouse_url))
    try:
        tenants = fetch_active_tenants(client, since, min_events=args.min_events)
    finally:
        client.close()
    print(f"{len(tenants)} tenant(s) with >= {args.min_events} events since {args.since}")

    def run_one(tenant: str) -> int:
        per = argparse.Namespace(**vars(args))
        per.tenant = tenant
        per.out = None
        return cmd_run(per)

    summary = run_all(tenants, run_one)
    print(f"run-all: {len(summary.ok)} ok, {len(summary.failed)} failed")
    for tenant in summary.failed:
        print(f"failed tenant: {tenant}", file=sys.stderr)
    return summary.exit_code


def cmd_train(args: argparse.Namespace) -> int:
    events = _load_events(args)
    if not events:
        print(f"no events for tenant {args.tenant} in window {args.since}", file=sys.stderr)
        return 1
    features = extract_features(events)
    model = train(features, contamination=args.contamination)
    save_model(model, args.out)
    print(f"trained on {len(features)} events; saved to {args.out}")
    return 0


def cmd_score(args: argparse.Namespace) -> int:
    events = _load_events(args)
    if not events:
        print(f"no events for tenant {args.tenant} in window {args.since}", file=sys.stderr)
        return 1
    features = extract_features(events)
    model = load_model(args.model)
    scores = score(model, features)

    indexed = sorted(zip(scores, events, strict=True), key=lambda p: -p[0])[: args.limit]
    out = [
        {
            "anomaly_score": float(s),
            "event": _serialise_event(e),
        }
        for s, e in indexed
    ]
    json.dump(out, sys.stdout, default=str, indent=2)
    print()
    return 0


def cmd_score_and_write(args: argparse.Namespace) -> int:
    """Score recent events and persist findings >= threshold to CH."""
    from clickhouse_connect import get_client  # noqa: PLC0415 - intentional lazy import

    from anomaly.writer import write_findings

    events = _load_events(args)
    if not events:
        print(f"no events for tenant {args.tenant} in window {args.since}", file=sys.stderr)
        return 1
    features = extract_features(events)
    model = load_model(args.model)
    scores = score(model, features)

    findings: list[dict] = []
    for s, ev in zip(scores, events, strict=True):
        if s < args.threshold:
            continue
        findings.append(
            {
                "occurred_at": ev.get("occurred_at"),
                "score": float(s),
                "event": ev,
            }
        )
    findings.sort(key=lambda f: -f["score"])
    findings = findings[: args.limit]

    if not findings:
        print(
            f"no findings >= {args.threshold} in window {args.since}; nothing written",
            file=sys.stderr,
        )
        return 0

    parsed = _parse_url(args.clickhouse_url)
    client = get_client(**parsed)
    try:
        written = write_findings(
            client, args.tenant, findings, model_version=args.model_version
        )
    finally:
        client.close()
    print(f"wrote {written} finding(s) to anomaly_findings")
    return 0


def _load_events(args: argparse.Namespace) -> list[dict]:
    """Lazily import clickhouse-connect so the CLI can be exercised in
    unit tests without the dependency.
    """
    from clickhouse_connect import get_client  # noqa: PLC0415 - intentional lazy import

    from anomaly.clickhouse_io import fetch_connection_events

    since = datetime.now(UTC) - _parse_window(args.since)
    parsed = _parse_url(args.clickhouse_url)
    client = get_client(**parsed)
    try:
        return fetch_connection_events(client, args.tenant, since)
    finally:
        client.close()


def _parse_window(spec: str) -> timedelta:
    if not spec:
        return timedelta(days=30)
    unit = spec[-1]
    try:
        n = int(spec[:-1])
    except ValueError as e:
        raise SystemExit(f"invalid --since value {spec!r}") from e
    if unit == "d":
        return timedelta(days=n)
    if unit == "h":
        return timedelta(hours=n)
    if unit == "m":
        return timedelta(minutes=n)
    raise SystemExit(f"unsupported --since unit {unit!r}; use d / h / m")


def _parse_url(url: str) -> dict:
    """Parse a ClickHouse URL into clickhouse-connect kwargs.

    clickhouse-connect speaks the HTTP interface only, so the default
    port is 8123 (8443 for https), never the native-protocol 9000.
    `clickhouse://` and `http://` are plain HTTP; `https://` is TLS.
    That lets the job reuse the controller's `http://` DSN as is.
    """
    from urllib.parse import urlparse

    parsed = urlparse(url)
    secure = parsed.scheme == "https"
    return {
        "host": parsed.hostname or "localhost",
        "port": parsed.port or (8443 if secure else 8123),
        "username": parsed.username or "default",
        "password": parsed.password or "",
        "database": parsed.path.lstrip("/") or "default",
        "secure": secure,
    }


def _serialise_event(e: dict) -> dict:
    out: dict = {}
    for k, v in e.items():
        if hasattr(v, "isoformat"):
            out[k] = v.isoformat()
        else:
            out[k] = v
    return out


if __name__ == "__main__":
    raise SystemExit(main())
