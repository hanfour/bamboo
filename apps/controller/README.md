# controller

Control plane: authentication, peer coordination, ACL evaluation, audit log.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

Pre-alpha skeleton. The binary builds, parses config, and starts an empty
gRPC server. Real handlers come online during Sprint 1–2.

## Build and run

```bash
# from this directory
go mod tidy
go build -o ./bin/controller ./cmd/controller

./bin/controller version
./bin/controller serve --config=config/example.yaml
```

Or run from the repo root via the Makefile:

```bash
make build
```

## Configuration

Default lookup path is `config/example.yaml`. Override with `--config`.
Secrets should come from environment variables, not the YAML file.

| Env var                      | Overrides                            |
| ---------------------------- | ------------------------------------ |
| `BAMBOO_GRPC_ADDR`           | `server.grpc_addr`                   |
| `BAMBOO_HTTP_ADDR`           | `server.http_addr`                   |
| `DATABASE_URL`               | `database.url`                       |
| `REDIS_URL`                  | `redis.url`                          |
| `OIDC_GOOGLE_CLIENT_ID`      | `auth.oidc.google.client_id`         |
| `OIDC_GOOGLE_CLIENT_SECRET`  | `auth.oidc.google.client_secret`     |
| `OIDC_GITHUB_CLIENT_ID`      | `auth.oidc.github.client_id`         |
| `OIDC_GITHUB_CLIENT_SECRET`  | `auth.oidc.github.client_secret`     |

## Stack

- Go 1.23+
- [cobra](https://github.com/spf13/cobra) for CLI
- [gRPC](https://grpc.io/) for client communication
- Postgres for state, Redis for sessions / rate limit
- ClickHouse for audit and telemetry (Sprint 3+)

## Layout

```
apps/controller/
  cmd/controller/      entry point + cobra commands
  internal/
    config/              YAML loader + env overrides
    server/              gRPC + HTTP listener wiring
  config/                example config files
  migrations/            SQL migrations (Sprint 1 #5)
  Dockerfile             multi-stage build
```

## Tracking

- [Sprint 1 — Issue #5](https://github.com/hanfour/bamboo/issues/5) DB schema
- [Sprint 1 — Issue #6](https://github.com/hanfour/bamboo/issues/6) gRPC proto
- [Sprint 1 — Issue #3](https://github.com/hanfour/bamboo/issues/3) NetBird import → `internal/`
