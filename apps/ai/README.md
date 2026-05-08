# ai

Tier-2 ML pipeline for bamboo: per-tenant anomaly detection over the
`connection_events` ClickHouse table. Tier-1 (rule-based) recommendations
live next to the controller in Go; this module is where the
ML-driven layer lands.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

Phase-2 starter scaffold. The package fits and scores Isolation
Forest models end-to-end on synthetic data; the CLI loader against a
live ClickHouse runs but is gated behind the network at the call site.

## Layout

```
apps/ai/
  pyproject.toml           build + ruff + pytest config
  src/anomaly/
    __init__.py            package surface
    features.py            deterministic feature extraction
    model.py               Isolation Forest pipeline (train / score / persist)
    clickhouse_io.py       thin reader for connection_events
    cli.py                 `bamboo-ai train` / `bamboo-ai score`
  tests/                   pytest suite (no live CH required)
  Dockerfile               python:3.12-slim build
```

## Local development

```bash
cd apps/ai
python3 -m venv .venv && source .venv/bin/activate
pip install -e '.[dev]'
pytest
ruff check src tests
```

## CLI

```bash
# Train a per-tenant model from the last 30 days of events.
bamboo-ai train \
  --tenant <uuid> \
  --since 30d \
  --out ./models/<uuid>.joblib

# Score recent events; print the top-10 most anomalous as JSON.
bamboo-ai score \
  --tenant <uuid> \
  --model ./models/<uuid>.joblib \
  --limit 10
```

The CLI talks to ClickHouse via `clickhouse-connect`. The DSN comes
from `--clickhouse-url` or the `CLICKHOUSE_URL` env var (defaults to
the dev compose host port).

## Why Isolation Forest

Per [ADR 0010 §Tier 2](../../docs/adr/0010-llm-multi-provider-strategy.md):

- Unsupervised — we have no labelled "attack" data to train on.
- Cheap at the volumes a single tenant produces; trains on commodity
  CPU in seconds for ~10⁵ events.
- Explainable enough that we can show evidence ("this peer's
  bytes_received was 6 standard deviations above its baseline at
  03:00 UTC"). A future Autoencoder layer can sit alongside it
  without forcing a refactor.

## How the model gets to the controller

Out of scope for this PR; the path is:

1. A scheduled job runs `bamboo-ai train` per tenant overnight,
   uploading `<uuid>.joblib` to a shared object store (S3 / GCS /
   filesystem in dev).
2. The controller (or a sibling Go binary) downloads the model and
   serves anomaly scores in-process. ML-in-Python, scoring-in-Go is
   possible via ONNX export; the simpler path is a thin Python
   sidecar that the Go service calls over gRPC.
3. Anomalies above a threshold become a fourth recommendation kind
   (`KIND_*` proto enum extension) — Tier-2 lives alongside the
   existing Tier-1 trio.

A follow-up ADR will pick the deployment shape.

## Tracking

- [ADR 0010 — LLM Multi-Provider Strategy](../../docs/adr/0010-llm-multi-provider-strategy.md)
- [ADR 0012 — Phase 1 → Phase 2 Transition](../../docs/adr/0012-phase-2-transition.md)
