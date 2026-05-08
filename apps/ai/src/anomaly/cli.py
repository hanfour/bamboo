# SPDX-License-Identifier: AGPL-3.0-or-later
"""Command-line entry point for the anomaly model.

Two subcommands:

  bamboo-ai train --tenant <id> [--since 30d] [--out PATH]
      Loads connection_events for the tenant from ClickHouse, fits an
      Isolation Forest, and saves the joblib bundle.

  bamboo-ai score --tenant <id> --model PATH [--limit 10]
      Loads recent events, scores them, prints the top anomalies as
      JSON for the controller / operator to ingest.

Production deployments will replace this CLI with a long-running
service; the CLI is the smallest path to a working end-to-end demo.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
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
            "CLICKHOUSE_URL", "clickhouse://bamboo:dev@localhost:19000/bamboo"
        ),
    )


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
    """Parse a clickhouse:// URL into clickhouse-connect kwargs."""
    from urllib.parse import urlparse

    parsed = urlparse(url)
    return {
        "host": parsed.hostname or "localhost",
        "port": parsed.port or 9000,
        "username": parsed.username or "default",
        "password": parsed.password or "",
        "database": parsed.path.lstrip("/") or "default",
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
