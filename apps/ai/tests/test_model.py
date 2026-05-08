# SPDX-License-Identifier: AGPL-3.0-or-later
from __future__ import annotations

import random
from datetime import UTC, datetime
from pathlib import Path

import pytest

from anomaly.features import extract_features
from anomaly.model import load_model, save_model, score, train


def _normal_event(rng: random.Random, hour: int):
    """Tightly clustered normal traffic — small RTT, modest byte counts."""
    return {
        "occurred_at": datetime(2026, 5, 7, hour, 0, 0, tzinfo=UTC),
        "event_type": "CONNECTION_ESTABLISHED",
        "path": "DIRECT_HOST",
        "bytes_sent": rng.randint(950, 1_050),
        "bytes_received": rng.randint(1_950, 2_050),
        "rtt_ms": rng.randint(8, 12),
    }


def _anomalous_event():
    """Pathologically far from the normal cluster on every numeric axis."""
    return {
        "occurred_at": datetime(2026, 5, 7, 3, 0, 0, tzinfo=UTC),
        "event_type": "CONNECTION_ESTABLISHED",
        "path": "DIRECT_HOST",
        "bytes_sent": 100_000_000,
        "bytes_received": 100_000_000,
        "rtt_ms": 30_000,
    }


def test_train_rejects_empty_dataframe():
    with pytest.raises(ValueError):
        train(extract_features([]))


def test_score_anomalies_rank_higher_than_normal_traffic():
    rng = random.Random(0)
    # 800 tightly-clustered normal events give the IF a clear baseline.
    train_events = [_normal_event(rng, (i % 24)) for i in range(800)]
    model = train(extract_features(train_events), contamination=0.05, random_state=0)

    test_events = [_normal_event(rng, 14) for _ in range(20)] + [_anomalous_event()]
    scores = score(model, extract_features(test_events))

    # The injected anomaly should be the unambiguous highest-scoring point.
    anomaly_score = scores[-1]
    normal_scores = scores[:-1]
    assert anomaly_score > normal_scores.max(), (
        f"anomaly {anomaly_score:.4f} not above normal max {normal_scores.max():.4f}"
    )
    # And the gap should clear normal noise. Isolation Forest scores are
    # bounded ~[0, 1]; a 5% margin above the highest normal point
    # corresponds to ~3 std-devs of the normal cluster on this fixture.
    assert anomaly_score >= normal_scores.max() * 1.05, (
        f"gap too small: anomaly {anomaly_score:.4f} vs normal max {normal_scores.max():.4f}"
    )


def test_save_and_load_round_trip(tmp_path: Path):
    rng = random.Random(1)
    events = [_normal_event(rng, (i % 24)) for i in range(200)]
    model = train(extract_features(events), random_state=1)

    path = tmp_path / "models" / "t.joblib"
    save_model(model, path)
    assert path.exists()

    reloaded = load_model(path)
    a = score(model, extract_features(events))
    b = score(reloaded, extract_features(events))
    assert (a == b).all()
