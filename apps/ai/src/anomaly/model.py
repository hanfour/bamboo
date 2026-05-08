# SPDX-License-Identifier: AGPL-3.0-or-later
"""Isolation Forest pipeline for connection_event anomaly detection.

The choice of Isolation Forest is deliberate per ADR 0010 §Tier 2:

  - unsupervised (we have no labelled "attack" data),
  - efficient at the volumes a single tenant produces,
  - explainable enough that we can show evidence for an anomaly.

A scikit-learn Pipeline owns:

  numeric -> StandardScaler
  categorical -> OneHotEncoder(handle_unknown="ignore")
  -> IsolationForest

The model is trained per tenant. Persistence is via joblib; the
filesystem path layout is the caller's concern (see cli.py).
"""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING

import joblib
from sklearn.compose import ColumnTransformer
from sklearn.ensemble import IsolationForest
from sklearn.pipeline import Pipeline
from sklearn.preprocessing import OneHotEncoder, StandardScaler

from anomaly.features import CATEGORICAL_COLUMNS, NUMERIC_COLUMNS

if TYPE_CHECKING:
    import numpy as np
    import pandas as pd


def _build_pipeline(contamination: float, random_state: int) -> Pipeline:
    """Compose the pre-processing + Isolation Forest pipeline."""

    pre = ColumnTransformer(
        [
            ("num", StandardScaler(), NUMERIC_COLUMNS),
            (
                "cat",
                OneHotEncoder(handle_unknown="ignore", sparse_output=False),
                CATEGORICAL_COLUMNS,
            ),
        ],
        remainder="drop",
        verbose_feature_names_out=False,
    )
    iso = IsolationForest(
        n_estimators=200,
        max_samples="auto",
        contamination=contamination,
        random_state=random_state,
        n_jobs=-1,
    )
    return Pipeline(steps=[("pre", pre), ("iso", iso)])


def train(
    features: pd.DataFrame,
    *,
    contamination: float = 0.05,
    random_state: int = 42,
) -> Pipeline:
    """Fit a fresh Isolation Forest pipeline on `features`.

    `contamination` is the prior expectation of how many points are
    anomalies. 5% is a defensible default for "well-behaved" tenant
    traffic; tune per tenant once feedback rolls in.

    Raises ValueError when `features` is empty: an unsupervised model
    cannot infer a normal baseline from nothing.
    """

    if features.empty:
        raise ValueError("cannot train on an empty feature frame")
    pipeline = _build_pipeline(contamination=contamination, random_state=random_state)
    pipeline.fit(features)
    return pipeline


def score(model: Pipeline, features: pd.DataFrame) -> np.ndarray:
    """Return anomaly scores for `features`.

    Higher values are more anomalous (we negate scikit-learn's
    `score_samples` so the sign matches operator intuition: positive
    bad, negative normal).
    """

    raw = model.score_samples(features)
    return -raw


def save_model(model: Pipeline, path: Path) -> None:
    """Persist `model` to `path` using joblib. Creates parents as needed."""
    path.parent.mkdir(parents=True, exist_ok=True)
    joblib.dump(model, path)


def load_model(path: Path) -> Pipeline:
    """Load a model previously persisted with save_model."""
    return joblib.load(path)
