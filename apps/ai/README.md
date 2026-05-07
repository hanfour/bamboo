# ai

AI / ML layer: anomaly detection, ACL recommendations, RCA.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

Pre-alpha scaffolding. No code yet — Phase 2 deliverable.

## Roadmap

- **Tier 1 (rule-based)**: unused ACL detection, statistical baselines
- **Tier 2 (ML)**: Autoencoder + Isolation Forest hybrid for anomaly detection,
  policy mining for least-privilege ACL recommendations
- **Tier 3 (LLM)**: natural-language ACL DSL, RCA assistant

## Stack

- Python 3.12
- Apache Flink or Bytewax for stream processing
- ONNX Runtime for inference
- vLLM (self-hosted LLM) or external API for Tier 3
