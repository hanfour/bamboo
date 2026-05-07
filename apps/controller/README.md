# controller

Control plane: authentication, peer coordination, ACL evaluation, audit log.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

Pre-alpha scaffolding. No code yet.

## Stack

- Go 1.23+
- gRPC (clients), REST (admin / public API)
- Postgres (state), Redis (sessions, rate limit)
- ClickHouse (audit + telemetry)

## Local development

TBD — wire up in Sprint 1.
