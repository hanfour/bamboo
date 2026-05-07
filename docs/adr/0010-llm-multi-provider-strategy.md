# 0010. LLM Multi-Provider Strategy

- **Status**: Accepted
- **Date**: 2026-05-07
- **Deciders**: founders

## Context

bamboo's AI surface area splits across five tasks (A1–A5):

| Task | Description                              | Needs LLM? |
| ---- | ---------------------------------------- | :--------: |
| A1   | Anomaly detection on connection traffic  | No (Autoencoder + Isolation Forest) |
| A2   | Least-privilege ACL recommendation       | No (rule mining over audit log)     |
| A3   | Unused ACL rule detection                | No (SQL on ClickHouse)              |
| A4   | Natural language → ACL DSL               | **Yes** — needs structured output reliability |
| A5   | RCA / connection failure explanation     | **Yes** — needs long context (full client log + ACL trace) |

The two LLM-dependent tasks have different requirements:

- **A4 (NL → ACL)**: low volume, high stakes. A wrong rule causes outages or
  security incidents. Structured-output and tool-calling reliability matter
  more than raw cost.
- **A5 (RCA)**: high volume, lower stakes per call (the user can see the
  explanation and judge it). Context length matters: a single RCA may
  ingest 30 minutes of client log + control plane events + ACL evaluation
  trace, easily exceeding 200K tokens.

Strategic constraints:

- Vendor lock-in to a single LLM provider is a risk (price changes, API
  deprecation, model retirement, geopolitical access).
- Some enterprise customers will reject any prompt-to-third-party-API
  configuration and require a self-hosted option.
- Data residency: APAC enterprise customers may require LLM inference within
  a specific jurisdiction.

## Decision

Build a provider-agnostic LLM abstraction in `apps/ai/llm/` and route tasks
to the best-fit provider:

| Task / Tier            | Provider                              | Model                  |
| ---------------------- | ------------------------------------- | ---------------------- |
| RCA / log summarization | Vertex AI (`asia-northeast1`)         | `gemini-2.5-pro`       |
| Lightweight RCA / weekly report | Vertex AI                  | `gemini-2.5-flash`     |
| NL → ACL DSL           | AWS Bedrock (`ap-northeast-1`)        | `claude-sonnet-4-6`    |
| Embeddings             | Vertex AI                             | `text-embedding-005`   |
| Privacy tier (self-hosted) | self-hosted on EKS GPU node       | `llama-3.1-8b-instruct` (vLLM) |

### Routing rules

1. **Default routing** is set per-task in configuration, not per-tenant.
2. Each tenant can opt into the **privacy tier** (self-hosted only). This
   disables Tier 3 LLM features that require frontier-model quality (e.g.
   NL → ACL falls back to a constrained-grammar parser).
3. **Enterprise tenants** can pin a specific provider region for residency
   compliance (e.g. Vertex AI Tokyo only, Bedrock Tokyo only).

### Why Vertex AI / Bedrock instead of direct APIs

- **Data residency**: Vertex AI and Bedrock both offer in-region inference
  with contractual data-handling boundaries. Direct Gemini API and
  Anthropic API have less explicit residency guarantees for APAC.
- **Enterprise procurement**: Vertex / Bedrock pass through existing GCP /
  AWS contracts; many enterprise customers cannot procure a separate
  contract with a model vendor.
- **Audit and logging**: managed cloud platforms offer audit log
  integration that direct APIs lack.

### Prompt and schema discipline

- All prompts and JSON schemas live in `apps/ai/llm/prompts/`, version-pinned.
- Every model call records: provider, model id, prompt hash, schema hash,
  input tokens, output tokens, latency, tenant. Stored in ClickHouse for
  cost analysis and rollback.
- Eval suite (`apps/ai/eval/`) runs golden cases against all configured
  providers on every release. New providers must pass a quality gate
  before becoming default for any task.

## Consequences

### Positive

- Optimal cost / quality per task: Gemini's 2M context wins for RCA,
  Claude's structured output reliability wins for ACL DSL generation.
- Insulation from any single provider's outage, price hike, or model
  deprecation.
- Privacy tier addresses regulated customers without rewriting AI features.
- Aligns with the cloud strategy in [ADR 0009](./0009-cloud-provider-strategy.md):
  Vertex AI uses GCP credentials only for AI inference (no core data on GCP);
  Bedrock fits naturally on the AWS core.

### Negative / Trade-offs

- Operational complexity: three sets of credentials (Vertex AI service
  account, Bedrock IAM, internal vLLM cluster).
- Quality consistency across providers requires a real eval harness from
  Phase 2 onward — not a "we'll add tests later" item.
- Self-hosted Llama 3.1 8B requires a GPU node on EKS. Spot instances
  reduce cost but add complexity (model loading time after preemption).
- Multi-provider abstraction layer must remain thin; over-abstracting
  loses access to provider-specific features (e.g. Gemini's video input,
  Claude's tool-use streaming).

### Neutral

- The abstraction interface is straightforward: prompt + schema in,
  structured output + metadata out.
- Most enterprise sales conversations now expect a "where is my data" answer;
  having Vertex Tokyo / Bedrock Tokyo / on-prem options strengthens this.

## Alternatives Considered

- **Gemini-only**: cheapest path. Rejected because (1) ACL DSL generation
  requires reliability we cannot yet guarantee on Gemini, (2) Google is a
  strategic competitor in the zero-trust space, (3) single-provider lock-in.
- **Claude-only**: best quality. Rejected because high-volume RCA at
  $3/M input is 2.4× more expensive than Gemini Pro for the same workload.
- **Self-hosted only (Llama / Qwen / DeepSeek)**: best privacy, lowest
  recurring cost. Rejected because Phase 2 ship velocity matters; we will
  not be able to fine-tune to frontier-model quality on NL → ACL within
  the timeline. Available as the privacy tier from the start.
- **OpenAI GPT-5**: comparable quality to Claude. Currently parked — could
  be added as a fourth provider later if pricing or capability shifts.

## Open Questions

- Cost ceiling per tenant per month for AI features — to be resolved in
  pricing decisions (Phase 2).
- Eval harness scope — start with NL → ACL golden cases, expand to RCA
  qualitative scoring in Phase 3.

## References

- [Gemini 2.5 pricing and context window](https://ai.google.dev/gemini-api/docs/pricing)
- [Anthropic Claude pricing](https://www.anthropic.com/pricing#anthropic-api)
- [Vertex AI generative AI pricing](https://cloud.google.com/vertex-ai/generative-ai/pricing)
- [AWS Bedrock model availability and pricing](https://aws.amazon.com/bedrock/pricing/)
- [LiteLLM (provider abstraction reference)](https://github.com/BerriAI/litellm)
- [vLLM (self-hosted serving)](https://github.com/vllm-project/vllm)
