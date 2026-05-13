# controller

Control plane: authentication, peer coordination, ACL evaluation, audit log,
telemetry + AI recommendations, and the REST bridge the Web admin console
consumes.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

Live in production (v0.1.7 on bamboo.miilink.net; v0.1.8 train staged on
main). gRPC + REST surfaces are implemented end-to-end:

- OIDC sign-in (Google + GitHub) + HMAC session JWT
- Peer registration / heartbeat / WatchPeers SSE
- Pre-auth keys (mint / list / revoke) with admin RBAC
- User invitations (mint / accept on OIDC callback / revoke / SMTP delivery)
- ACL policy (HCL DSL, REST GET + PUT, optimistic concurrency)
- DNS settings read
- Activity feed + ClickHouse policy-evaluation log query
- AI recommendations (rule-based Tier-1 + Tier-2 anomaly findings from
  the Python pipeline under `apps/ai`)
- Relay-token mint (HMAC, peer-bound) for the DERP-style relay
- Peer-session bearer issued at Register time; gates heartbeat /
  watch / relay-token under `BAMBOO_REQUIRE_AUTH=true`

## Build and run

```bash
# from this directory
go build -o ./bin/controller ./cmd/controller

./bin/controller version
./bin/controller serve --config=config/example.yaml
```

Or run from the repo root via the Makefile / docker compose:

```bash
make build
make local-up          # full stack: controller + postgres + clickhouse + web
make local-bootstrap   # mint a pre-auth key + register a test peer
```

## Configuration

Default lookup path is `config/example.yaml`. Override with `--config`.
Secrets should come from environment variables, not the YAML file.

| Env var                      | Overrides                            |
| ---------------------------- | ------------------------------------ |
| `BAMBOO_GRPC_ADDR`           | `server.grpc_addr`                   |
| `BAMBOO_HTTP_ADDR`           | `server.http_addr`                   |
| `BAMBOO_REQUIRE_AUTH`        | `auth.require_auth`                  |
| `DATABASE_URL`               | `database.url`                       |
| `CLICKHOUSE_DSN`             | `clickhouse.dsn` (optional)          |
| `OIDC_GOOGLE_CLIENT_ID`      | `auth.oidc.google.client_id`         |
| `OIDC_GOOGLE_CLIENT_SECRET`  | `auth.oidc.google.client_secret`     |
| `OIDC_GITHUB_CLIENT_ID`      | `auth.oidc.github.client_id`         |
| `OIDC_GITHUB_CLIENT_SECRET`  | `auth.oidc.github.client_secret`     |
| `BAMBOO_SMTP_HOST`           | `smtp.host` (invitation email)       |
| `BAMBOO_SMTP_PORT`           | `smtp.port`                          |
| `BAMBOO_SMTP_USER`           | `smtp.user`                          |
| `BAMBOO_SMTP_PASS`           | `smtp.pass`                          |
| `BAMBOO_SMTP_FROM`           | `smtp.from`                          |
| `BAMBOO_SMTP_PUBLIC_BASE_URL`| `smtp.public_base_url` (invite link) |

`BAMBOO_REQUIRE_AUTH=true` is the prod-mode gate: peer-onboarding
endpoints (REST + gRPC register / heartbeat / watch + REST relay-token)
require a pre-auth key, peer-session bearer, or user-session JWT. Slug-
only callers get 401. See `docs/development/project-understanding-
2026-05-13.md` for the failure modes this closes.

## Stack

- Go 1.23+
- gRPC (`bamboo.v1`) + a parallel REST JSON bridge under `/api/v1/*`
- Postgres for tenant / peer / policy / audit / invitations
- ClickHouse for `evaluation_traces` + `anomaly_findings` (degrades to
  no-op when unconfigured)
- HMAC-SHA256 session / relay / peer-session tokens
- HCL DSL for ACL policy (`apps/controller/internal/policy`)
- `cobra` CLI

## Layout

```
apps/controller/
  cmd/controller/      entry point + cobra commands
  internal/
    auth/                OIDC + HMAC tokens (session / relay / peer-session)
    clickhouse/          traces + anomaly_findings driver
    config/              YAML loader + env overrides
    db/repo/             Postgres repositories (tenants / users / peers / ...)
    events/              in-memory pub-sub for WatchPeers
    handlers/            gRPC service implementations
    ipalloc/             tenant subnet IP allocation
    mail/                SMTP invitation email sender (stdlib net/smtp)
    policy/              HCL parser + Allow() L3 enforcement
    server/              HTTP + gRPC listener wiring + REST bridge
    wgsync/              wg state ingestion
  migrations/            SQL (00001-00007, run via goose)
  test/e2e/              integration tests (require DATABASE_URL_TEST)
  Dockerfile             multi-stage build
```

## Tests

```bash
go test ./...
DATABASE_URL_TEST="postgres://bamboo:dev@127.0.0.1:15432/bamboo?sslmode=disable" \
  go test ./test/e2e/...
```

The `test/e2e` package skips when `DATABASE_URL_TEST` is unset.
