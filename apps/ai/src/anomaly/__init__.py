# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tier-2 anomaly detection for bamboo.

Trains a per-tenant Isolation Forest over connection_events, scores new
events at inference time, and surfaces high-anomaly clusters as
recommendations through the controller's PolicyService.

The package is intentionally framework-light: feature extraction is
deterministic (no external services), the model is a plain
scikit-learn Pipeline, and the ClickHouse loader is the only IO
dependency.
"""

from anomaly.features import FEATURE_COLUMNS, extract_features
from anomaly.model import load_model, score, train

__all__ = [
    "FEATURE_COLUMNS",
    "extract_features",
    "load_model",
    "score",
    "train",
]
