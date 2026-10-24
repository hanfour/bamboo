# 0015. AI Tier 2 — a nightly batch job, not a service

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: founders
- **Related**: [ADR 0010](./0010-llm-multi-provider-strategy.md) §Tier 2,
  [ADR 0012](./0012-phase-2-transition.md), `apps/ai/README.md`,
  `docs/design/2026-09-23-project-goals.md` (P2 "AI Tier 2 delivery").

## Context

`apps/ai` fits a per-tenant Isolation Forest over ClickHouse
`connection_events`. `bamboo-ai run --tenant <id>` trains one tenant and
writes rows at score >= 0.6 into `anomaly_findings`. The controller reads
that table for the last 24 hours and shows each row as a
`KIND_FLAG_ANOMALOUS` recommendation. The model never changes policy.

Nothing ran that command in a deployed stack. The README asked operators
to add one cron line per tenant, so a new tenant got no detection until
someone edited crontab. No `bamboo-ai` image was published, and the
single-VPS compose file had no service for it.

The CLI also defaulted to ClickHouse's native port (9000). Its client,
`clickhouse-connect`, speaks only the HTTP interface, so the documented
cron line could not have connected.

## Decision

1. **Batch, not a service.** Training is a few seconds of CPU per tenant
   per day. A long-running process would hold memory all day to do that.
   The job runs once a night and exits.
2. **`bamboo-ai run-all` discovers tenants from ClickHouse.** It trains
   every tenant with at least `--min-events` (default 50) events in the
   window (default 7 days). ClickHouse is where the training data lives,
   so the job needs no Postgres credentials and no RLS bypass role. A
   tenant below the floor is skipped, because a model fitted to a handful
   of events flags noise.
3. **One tenant failing does not stop the rest.** Each tenant runs in
   turn. The job exits non-zero when any tenant failed, so systemd marks
   the run failed and `systemctl status` shows it.
4. **Single-VPS wiring follows the backup timer.** The compose file gets
   an `ai` service under the `jobs` profile, so `docker compose up` does
   not start it. `bamboo-ai.timer` runs
   `docker compose --profile jobs run --rm ai` at 04:15 UTC, after the
   03:30 backup, at low CPU and IO priority. `bootstrap.sh` installs it.
5. **The image ships with the others.** `images.yml` publishes
   `ghcr.io/<owner>/bamboo-ai` with the same tags as controller, web and
   relay, so `BAMBOO_IMAGE_TAG` pins all four. `deploy.sh` pulls it but
   does not fail a deploy when an older tag has no AI image.
6. **Models are disposable.** Each run retrains from scratch. The model
   file goes to the `ai-models` volume for inspection, and losing it costs
   nothing. Findings are the only output that matters, and ClickHouse
   expires them after 30 days.

## Consequences

### Positive

- A new tenant gets anomaly findings the night after it has enough
  traffic, with no operator step.
- The job uses the same image tag and deploy path as the rest of the
  stack.
- The CLI works against the HTTP DSN the controller already uses.

### Negative / Trade-offs

- Findings are up to a day old. The controller shows the last 24 hours,
  so a daily run keeps the list full, but a spike at 05:00 surfaces the
  next morning.
- Tenants run one after another. At hundreds of tenants the run gets
  long; that is the point to parallelize or move to a queue.
- `run-all` reads every tenant's events with one ClickHouse user. It is
  a trusted backend job, the same as the controller's own reads.

### Neutral

- Helm is not covered. A CronJob running the same image and command is
  the natural port when the chart needs it.

## Alternatives Considered

- **A scheduler inside the controller.** Rejected: it would put a Python
  ML stack, or a subprocess call into one, inside a distroless Go
  binary, and tie training CPU to the API process.
- **Tenant list from Postgres.** Rejected: it needs a second set of
  credentials and the `bamboo_maintenance` bypass role from ADR 0014,
  to list tenants that may have no events to train on.
- **Keep per-tenant cron lines.** Rejected: every new tenant needs a
  manual edit, which is exactly the gap this closes.

## References

- `apps/ai/src/anomaly/cli.py` (`run-all`)
- `infra/full/bamboo-ai.service`, `infra/full/bamboo-ai.timer`
