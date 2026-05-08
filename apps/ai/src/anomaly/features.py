# SPDX-License-Identifier: AGPL-3.0-or-later
"""Feature extraction for connection_events.

The output of `extract_features` is the DataFrame we feed to
scikit-learn. Every transformation here is deterministic; randomness
lives in the model layer.

Columns produced:

  - bytes_sent_log     log10(1 + bytes_sent)
  - bytes_received_log log10(1 + bytes_received)
  - rtt_ms             clamped to [0, 60_000]
  - hour_sin           sin(2π · hour_of_day / 24)
  - hour_cos           cos(2π · hour_of_day / 24)
  - event_type         categorical, one-hot at fit time
  - path               categorical, one-hot at fit time

We avoid integer encoding for categoricals so a future model swap
(e.g., gradient-boosted trees) does not have to undo an arbitrary
ordering.
"""

from __future__ import annotations

from collections.abc import Iterable
from typing import Any

import numpy as np
import pandas as pd

NUMERIC_COLUMNS: list[str] = [
    "bytes_sent_log",
    "bytes_received_log",
    "rtt_ms",
    "hour_sin",
    "hour_cos",
]
CATEGORICAL_COLUMNS: list[str] = ["event_type", "path"]
FEATURE_COLUMNS: list[str] = NUMERIC_COLUMNS + CATEGORICAL_COLUMNS

_RTT_MS_MAX = 60_000


def extract_features(events: Iterable[dict[str, Any]]) -> pd.DataFrame:
    """Turn an iterable of connection_event dicts into a feature frame.

    Each dict must carry: occurred_at (datetime), event_type (str),
    path (str), bytes_sent (int), bytes_received (int), rtt_ms (int).
    Missing fields fall back to safe defaults.
    """

    rows: list[dict[str, Any]] = []
    for e in events:
        occurred_at = _coerce_datetime(e.get("occurred_at"))
        hour = float(occurred_at.hour) if occurred_at is not None else 0.0
        bytes_sent = max(int(e.get("bytes_sent") or 0), 0)
        bytes_received = max(int(e.get("bytes_received") or 0), 0)
        rtt_ms = min(max(int(e.get("rtt_ms") or 0), 0), _RTT_MS_MAX)

        rows.append(
            {
                "bytes_sent_log": np.log10(1 + bytes_sent),
                "bytes_received_log": np.log10(1 + bytes_received),
                "rtt_ms": float(rtt_ms),
                "hour_sin": np.sin(2 * np.pi * hour / 24.0),
                "hour_cos": np.cos(2 * np.pi * hour / 24.0),
                "event_type": str(e.get("event_type") or "UNSPECIFIED"),
                "path": str(e.get("path") or "UNSPECIFIED"),
            }
        )

    if not rows:
        return pd.DataFrame(columns=FEATURE_COLUMNS)
    return pd.DataFrame(rows, columns=FEATURE_COLUMNS)


def _coerce_datetime(value: Any) -> Any:
    """Best-effort conversion to a datetime-like with .hour. Returns
    None on failure so the caller substitutes the default.
    """
    if value is None:
        return None
    if hasattr(value, "hour"):
        return value
    try:
        return pd.to_datetime(value)
    except (TypeError, ValueError):
        return None
