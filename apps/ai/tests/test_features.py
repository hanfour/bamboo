# SPDX-License-Identifier: AGPL-3.0-or-later
from __future__ import annotations

from datetime import UTC, datetime

import numpy as np
import pytest

from anomaly.features import (
    CATEGORICAL_COLUMNS,
    FEATURE_COLUMNS,
    NUMERIC_COLUMNS,
    extract_features,
)


def _event(**overrides):
    base = {
        "occurred_at": datetime(2026, 5, 7, 12, 0, 0, tzinfo=UTC),
        "event_type": "CONNECTION_ESTABLISHED",
        "path": "DIRECT_HOST",
        "bytes_sent": 1024,
        "bytes_received": 2048,
        "rtt_ms": 25,
    }
    base.update(overrides)
    return base


def test_extract_features_columns_in_canonical_order():
    df = extract_features([_event()])
    assert list(df.columns) == FEATURE_COLUMNS
    for col in NUMERIC_COLUMNS:
        assert df[col].dtype.kind in "fi"
    for col in CATEGORICAL_COLUMNS:
        assert df[col].dtype == object


def test_log_scale_for_byte_counters():
    df = extract_features([_event(bytes_sent=0, bytes_received=999)])
    assert df.iloc[0]["bytes_sent_log"] == 0.0
    assert df.iloc[0]["bytes_received_log"] == pytest.approx(np.log10(1000), rel=1e-6)


def test_rtt_clamped_to_sane_ceiling():
    df = extract_features([_event(rtt_ms=10**9)])
    assert df.iloc[0]["rtt_ms"] == 60_000


def test_negative_inputs_are_treated_as_zero():
    df = extract_features([_event(bytes_sent=-5, rtt_ms=-12)])
    row = df.iloc[0]
    assert row["bytes_sent_log"] == 0.0
    assert row["rtt_ms"] == 0.0


def test_hour_cyclical_encoding_is_unit_circle():
    df = extract_features(
        [
            _event(occurred_at=datetime(2026, 5, 7, h, 0, 0, tzinfo=UTC))
            for h in range(24)
        ]
    )
    radii = df["hour_sin"] ** 2 + df["hour_cos"] ** 2
    assert np.allclose(radii, 1.0, atol=1e-9)


def test_missing_fields_get_defaults():
    df = extract_features([{"occurred_at": None, "event_type": None, "path": None}])
    assert df.iloc[0]["event_type"] == "UNSPECIFIED"
    assert df.iloc[0]["path"] == "UNSPECIFIED"
    assert df.iloc[0]["bytes_sent_log"] == 0.0
    assert df.iloc[0]["rtt_ms"] == 0.0


def test_empty_iterable_yields_empty_frame_with_canonical_columns():
    df = extract_features([])
    assert df.empty
    assert list(df.columns) == FEATURE_COLUMNS
