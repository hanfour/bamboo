# Local end-to-end deployment

This walkthrough brings up the entire bamboo stack — controller, web,
relay, Postgres, ClickHouse — on your dev machine via docker-compose
and lets you connect Mac + iPhone clients to it over your LAN. Use it
to validate deploys end-to-end before trying them on a real VPS or
AWS.

## Prerequisites

- Docker (with `docker compose`)
- A built bamboo binary on the host: `make build` produces
  `./bin/controller` which the bootstrap script invokes for migrations
  and (optionally) `grpcurl` for minting the first preauth-key.
- Optional but useful: `grpcurl` on PATH for preauth-key generation
  (`go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest`).

## One-shot bring-up

```bash
make local-up         # docker compose up; waits for postgres + clickhouse health
make local-bootstrap  # runs migrations, mints first preauth-key, prints URLs
```

The bootstrap script prints something like:

```
Controller HTTP:  http://localhost:8081
Controller gRPC:  localhost:8080
Web UI:           http://localhost:3000
Relay (WS):       ws://localhost:18443/relay
Preauth key:      bka_aBcD12_ef34GhIjKlMnOpQ...
```

Open http://localhost:3000 in a browser to see the Web UI, register
peers from the bamboo CLI or the macOS / iOS app using the printed
preauth key.

## Connecting Mac + iPhone over the LAN

The controller binds to `0.0.0.0` so devices on the same Wi-Fi can
reach it.

```bash
# On the host (macOS):
ipconfig getifaddr en0    # -> e.g. 192.168.1.42
```

In the bamboo app (macOS or iOS):

- **Controller URL**: `http://192.168.1.42:8081`
- **Tenant slug**: `default`
- **Pre-auth key**: paste the value bootstrap printed
- **Relay URL** (optional, for cellular fallback testing):
  `ws://192.168.1.42:18443/relay`

Tap **Connect**. The first time, the OS asks to install a VPN
configuration; approve it. The status flips to `Connected`.

## Auth mode (local vs prod)

The local compose pins `BAMBOO_REQUIRE_AUTH=false`, which keeps the
permissive dev path on: REST `/api/v1/*` accepts an `X-Tenant-Slug`
header and auto-provisions the `default` tenant on first hit. Web UI
loads without sign-in, and the bootstrap script can mint a preauth-key
without an admin session.

The prod compose (`infra/full/docker-compose.yml`) defaults to
`BAMBOO_REQUIRE_AUTH=true` — the opposite. If you're using this local
stack to reproduce a prod issue around auth, flip the local compose
to `"true"` to match the prod surface.

## Tear down

```bash
make local-down       # stops everything; deletes the postgres + clickhouse volumes
```

## Iterating on code

The compose file references `ghcr.io/hanfour/bamboo-{controller,relay,web}:main`
images by default. To run un-pushed local code, edit
`infra/local/docker-compose.yml` and uncomment the `build:` blocks
under each service, then:

```bash
docker compose -f infra/local/docker-compose.yml build controller
docker compose -f infra/local/docker-compose.yml up -d controller
```

For really tight Go iteration on the controller, skip the container
entirely:

```bash
make local-up                                                 # postgres+clickhouse only
DATABASE_URL=postgres://bamboo:dev@localhost:15432/bamboo?sslmode=disable \
CLICKHOUSE_URL=http://bamboo:dev@localhost:18123/bamboo \
BAMBOO_SESSION_SECRET=local-dev-session-secret-32-bytes-pad-padding-padding \
BAMBOO_BASE_URL=http://localhost:8081 \
  ./bin/controller serve
```

The Web UI similarly:

```bash
cd apps/web
BAMBOO_API_URL=http://localhost:8081 BAMBOO_TENANT=default npm run dev
```

## What's exercised

| Component         | Image / source                        |
| ----------------- | ------------------------------------- |
| Postgres          | `postgres:16-alpine`                  |
| ClickHouse        | `clickhouse/clickhouse-server:24.8`   |
| Controller        | `ghcr.io/hanfour/bamboo-controller`   |
| Web UI            | `ghcr.io/hanfour/bamboo-web`          |
| Relay             | `ghcr.io/hanfour/bamboo-relay`        |

Once everything works locally, the same images ship to the VPS
(infra/relay/) or AWS (infra/terraform/) deploys without code changes
— only env vars differ.

## Known gaps

- No TLS in this stack. The controller speaks plain HTTP on `:8081`
  and the relay is on `--dev-plaintext` mode. Production deploys put
  Caddy in front (see `infra/relay/Caddyfile` for the pattern).
- OIDC sign-in won't work over LAN-IP URLs because Google / GitHub
  reject non-DNS redirect URIs. Use the X-Tenant-Slug header (default
  tenant) or pre-auth keys for local testing; OIDC needs a real
  domain + TLS.
- The first `make local-bootstrap` run after `make local-up` may need
  ~20 seconds to wait for ClickHouse to be ready. Retry safely if it
  times out.
