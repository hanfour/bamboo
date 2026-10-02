# ai

Tier-2 ML pipeline for bamboo: per-tenant anomaly detection over the
`connection_events` ClickHouse table. Tier-1 (rule-based) recommendations
live next to the controller in Go; this module is where the
ML-driven layer lands.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

The package fits and scores Isolation Forest models. `bamboo-ai run`
trains one tenant and writes rows into ClickHouse `anomaly_findings`.
The controller already reads that table (last 24 hours, score >= 0.6)
and surfaces each row as a `KIND_FLAG_ANOMALOUS` recommendation next
to the three rule-based kinds. Nothing inside the controller process
starts the training; run the command on a schedule.

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

# Train and write findings the controller will show as FLAG_ANOMALOUS.
bamboo-ai run --tenant <uuid> --since 7d
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

## How a score becomes a recommendation

1. Cron (or `scripts/run-tenant.sh`) runs `bamboo-ai run --tenant <uuid>`
   against the tenant's ClickHouse. The model file stays on the
   machine that runs the job (`BAMBOO_AI_MODEL_DIR`, default `./models`).
2. Findings at score >= 0.6 are inserted into `anomaly_findings`.
   The controller creates that table on boot and only reads it.
3. `ListRecommendations` appends `recommend.Anomalies` for findings
   generated in the last 24 hours. The admin UI shows them as
   `FLAG_ANOMALOUS`. The diff is empty: the operator triages, the
   model does not change policy.

```bash
# nightly, one line per tenant
0 3 * * * BAMBOO_AI_MODEL_DIR=/var/lib/bamboo/models \
  CLICKHOUSE_URL=clickhouse://bamboo:dev@clickhouse:9000/bamboo \
  /opt/bamboo/apps/ai/scripts/run-tenant.sh <tenant-uuid>
```

```bash
bamboo-ai run --tenant <uuid> --since 7d --threshold 0.6
```

## Tracking

- [ADR 0010 — LLM Multi-Provider Strategy](../../docs/adr/0010-llm-multi-provider-strategy.md)
- [ADR 0012 — Phase 1 → Phase 2 Transition](../../docs/adr/0012-phase-2-transition.md)
