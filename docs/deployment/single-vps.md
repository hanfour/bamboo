# Single-VPS deployment

End-to-end recipe for running the entire bamboo stack — controller,
web, relay, Postgres, ClickHouse, Caddy + Let's Encrypt — on one
VPS. Aim is "first user dogfood that works on cellular": Tailscale
replacement at $5–11/month.

If you only need same-LAN testing, use [`docs/deployment/local.md`](./local.md)
instead.

For a multi-region production deployment that scales beyond ~10
concurrent users, follow [`infra/terraform/`](../../infra/terraform/)
to provision AWS Tokyo per [ADR 0009](../adr/0009-cloud-provider-strategy.md).

## What you'll spend

| Item | Provider | Monthly |
| ---- | -------- | ------- |
| VPS (4GB / 2 vCPU) | Hetzner CX22 / Vultr Tokyo | ~$5–7 |
| Domain | Cloudflare / Porkbun | $1 amortized |
| **Total** | | **$6–8** |

## What you'll need before starting

- A VPS with a public IPv4 address. 4GB RAM minimum (Postgres +
  ClickHouse + controller + web + relay all on one host).
- A domain you control. We use two subdomains:
    - `bamboo.yourdomain.com` — Web UI + controller HTTP/REST + OIDC
    - `relay.yourdomain.com`  — relay WebSocket
- Both DNS A records pointing at the VPS's public IP. **Make sure
  the Cloudflare proxy is OFF** (orange cloud → grey cloud), else
  Let's Encrypt's HTTP-01 challenge fails.
- Docker + docker compose installed on the VPS.

## Steps

### 1. Push images to ghcr.io

If you've enabled the `Build + publish images` GitHub Actions
workflow, every push to `main` already publishes
`ghcr.io/<owner>/bamboo-{controller,web,relay}:main` and (on tags)
`:vX.Y.Z`. Make the org's package visibility "public" once and
you're done.

If not yet, build + push manually from your dev machine:

```bash
cd /path/to/bamboo
docker build -t ghcr.io/hanfour/bamboo-controller:latest -f apps/controller/Dockerfile .
docker build -t ghcr.io/hanfour/bamboo-web:latest -f apps/web/Dockerfile .
docker build -t ghcr.io/hanfour/bamboo-relay:latest -f infra/relay/Dockerfile .
docker push ghcr.io/hanfour/bamboo-controller:latest
docker push ghcr.io/hanfour/bamboo-web:latest
docker push ghcr.io/hanfour/bamboo-relay:latest
```

### 2. Copy the deploy bundle to the VPS

```bash
scp -r infra/full vps:/opt/bamboo
ssh vps
cd /opt/bamboo
```

### 3. Generate secrets + fill in `.env`

```bash
cp .env.example .env
$EDITOR .env
```

Fill in:

```bash
DOMAIN=bamboo.yourdomain.com
RELAY_DOMAIN=relay.yourdomain.com
BAMBOO_SESSION_SECRET=$(openssl rand -base64 48)   # paste the output
# Dedicated relay-token key — set it to a DIFFERENT value so a relay-host
# compromise can't forge controller session JWTs (recommended). If you
# omit it, the stack falls back to BAMBOO_SESSION_SECRET.
BAMBOO_RELAY_SECRET=$(openssl rand -base64 48)      # paste the output
POSTGRES_PASSWORD=$(openssl rand -base64 24)
CLICKHOUSE_PASSWORD=$(openssl rand -base64 24)

# Optional, leave empty for now
OIDC_GOOGLE_CLIENT_ID=
OIDC_GOOGLE_CLIENT_SECRET=
OIDC_GITHUB_CLIENT_ID=
OIDC_GITHUB_CLIENT_SECRET=
```

### 4. Open firewall ports

```bash
sudo ufw allow 80/tcp 443/tcp
```

Caddy needs port 80 only during cert issuance. Both ports stay open
afterwards because:
- 80 → Caddy redirects HTTP to HTTPS
- 443 → Web UI + controller REST + relay WSS all multiplex here

### 5. Bring it up

```bash
docker compose up -d
docker compose logs -f
```

First boot takes ~60 seconds for Caddy to obtain Let's Encrypt
certificates. Watch for `certificate obtained successfully` for both
domains. Once that's done, ctrl-C the log stream.

### 6. Bootstrap (run once)

```bash
./bootstrap.sh
```

Output:

```
==> running migrations
==> waiting for https://bamboo.yourdomain.com/healthz
==> minting preauth-key

================================================================
bamboo VPS deployment ready
================================================================
Web UI:           https://bamboo.yourdomain.com
OIDC sign-in:     https://bamboo.yourdomain.com/auth/google/login
Relay (WS):       wss://relay.yourdomain.com/relay
Preauth key:      bka_aBcD12_efGhIjKlMnOpQrStUv... (TTL 30 days)
```

### 7. Connect from Mac / iPhone

In the bamboo app's Settings:

- **Controller URL**: `https://bamboo.yourdomain.com`
- **Tenant slug**: `default`
- **Pre-auth key**: paste the value from bootstrap
- **Relay URL**: `wss://relay.yourdomain.com/relay`

Tap **Connect**. The first time on each device, the OS prompts to
add a VPN configuration; approve. Status flips to **Connected**.

This works from anywhere — home Wi-Fi, cafe, cellular, hotel — as
long as the device can reach the public internet.

## Operations

### Watching logs

```bash
docker compose logs -f --tail=200 controller
docker compose logs -f --tail=200 caddy
docker compose logs -f --tail=200 relay
```

### Updating to a new image

The compose file pins controller / web / relay to `:latest`, which
tracks the most recent **release tag**, not main HEAD. Builds for
`vX.Y.Z` tag pushes update `:latest`; main pushes only update
`:main` and `:<short-sha>` (see `.github/workflows/images.yml`).
Routine upgrades therefore mean "wait for a new release tag, then
pull". To test main HEAD instead, override the image to `:main` —
but `:latest` is the supported deploy target.

`serve` does NOT auto-run migrations on boot, so an upgrade that
introduces new schema columns needs an explicit `migrate up`
between pulling the new image and recreating the controller.
Otherwise the new controller will boot against an old schema and
its first read query against the new columns will fail.

The full upgrade flow:

```bash
cd /opt/bamboo/infra/full

# (Recommended) snapshot the DB so a failed migration can roll back.
docker compose exec -T postgres pg_dump -U bamboo bamboo \
  | gzip > "/home/ubuntu/bamboo-pre-$(date +%Y%m%d-%H%M).sql.gz"

# Pull the new images. This does not restart anything yet.
docker compose pull controller web relay

# Apply any pending migrations using the new controller image. The
# old controller stays up against the same DB during this step —
# additive migrations (the only kind we ship) are safe for the old
# controller's read paths.
docker compose run --rm controller migrate status   # confirm what's Pending
docker compose run --rm controller migrate up

# Recreate controller + web with the new images. ~5s downtime.
docker compose up -d controller web relay
docker compose ps                                    # all services Up
```

If something looks wrong after `migrate up` but before recreate,
roll the schema back with `docker compose run --rm controller migrate down`
and re-pull the previous image tag.

### Backing up Postgres

`bootstrap.sh` installs a **nightly backup timer** (`bamboo-backup.timer`,
03:30 UTC) that runs [`backup.sh`](../../infra/full/backup.sh): it
`pg_dump`s the `bamboo` database, verifies the gzip, writes a timestamped
copy to `/var/backups/bamboo`, and keeps the newest 14 (older ones roll
off). Postgres is the only durable store backed up — ClickHouse holds
best-effort telemetry that's intentionally out of scope.

Check it's armed and run one on demand:

```bash
systemctl list-timers bamboo-backup.timer     # next fire time
sudo systemctl start bamboo-backup.service     # dump right now
ls -lh /var/backups/bamboo                     # the dumps
```

**Off-box is what saves you if the VPS dies.** Local dumps live on the
same disk as the database — they protect against a bad migration or an
`DROP TABLE` accident, not against losing the box. To ship each dump off
the box, install the `aws` CLI on the host and set in `.env`:

```bash
BACKUP_S3_URI=s3://your-bucket/bamboo
# For Cloudflare R2 / MinIO / any S3-compatible store, also set:
BACKUP_S3_ENDPOINT=https://<accountid>.r2.cloudflarestorage.com
```

(Provide the bucket credentials the standard way — `aws configure`, an
instance role, or `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` in the
service environment.) Let the bucket's lifecycle policy own long-term
retention; `backup.sh` only rotates the local copies.

### Restoring from a backup

```bash
cd /opt/bamboo/infra/full     # or wherever your compose file lives

# 1. Pick a dump (local, or `aws s3 cp` one back from your bucket first).
DUMP=/var/backups/bamboo/bamboo-20260705-033000.sql.gz

# 2. Stop the controller so nothing writes mid-restore.
docker compose stop controller web relay

# 3. Drop + recreate the schema, then load the dump. pg_dump output is
#    plain SQL, so psql replays it. --set ON_ERROR_STOP=1 aborts on the
#    first error instead of limping through a half-restore.
gunzip -c "$DUMP" \
  | docker compose exec -T postgres \
      psql -U bamboo -d bamboo --set ON_ERROR_STOP=1 -1

# 4. Bring the stack back and sanity-check.
docker compose up -d controller web relay
docker compose run --rm controller migrate status   # should be all Applied
```

If the dump predates the running image's schema, run `migrate up` after
step 3 to re-apply any newer migrations on top of the restored data.

### Adding OIDC sign-in

Once you've registered an OAuth client in
console.cloud.google.com / GitHub developer settings:

```bash
# Edit /opt/bamboo/.env
OIDC_GOOGLE_CLIENT_ID=...
OIDC_GOOGLE_CLIENT_SECRET=...

# Roll the controller
docker compose up -d --force-recreate controller
```

Redirect URIs to register in the Google / GitHub console:

- `https://bamboo.yourdomain.com/auth/google/callback`
- `https://bamboo.yourdomain.com/auth/github/callback`

### Auth mode

The prod compose defaults to `BAMBOO_REQUIRE_AUTH=true`, which makes
the REST `/api/v1/*` surface reject unauthenticated requests with 401
instead of falling back to the legacy `X-Tenant-Slug` dev path. Web UI
sessions (Google / GitHub OIDC), Apple-app bearer tokens, and the
peer-onboarding pre-auth-key flows all keep working unchanged — only
the bare-header dev path is gated off.

To verify after deploy:

```bash
# From a machine that isn't signed in:
curl -i https://bamboo.yourdomain.com/api/v1/peers -H "X-Tenant-Slug: default"
# Expected: HTTP/2 401  with body {"error":"authentication required"}

# /api/v1/me still responds so the Web can render its signed-out landing:
curl -i https://bamboo.yourdomain.com/api/v1/me
# Expected: HTTP/2 200  with body {"authenticated":false,...}
```

If you need to temporarily flip the gate off (e.g. to reproduce a
dev-mode bug), uncomment `BAMBOO_REQUIRE_AUTH=false` in `.env` and
`docker compose up -d --force-recreate controller`. Don't leave it
off in production — known bypass paths for peer onboarding and the
relay-token endpoint will be closed in a follow-up PR (issue #135
phase 2), but the gate-on-by-default state is the safer baseline
even before that.

### Rotating BAMBOO_SESSION_SECRET

```bash
# Generate new secret
NEW=$(openssl rand -base64 48)

# Edit .env, replace BAMBOO_SESSION_SECRET
$EDITOR .env

# Roll the controller (signs session JWTs) — and the relay too if you
# left BAMBOO_RELAY_SECRET unset, since it then falls back to this key.
docker compose up -d --force-recreate controller relay
```

All existing session tokens invalidate; users have to sign in again.

### Rotating BAMBOO_RELAY_SECRET

The relay-token key is isolated from the session secret (audit C-1), so
you can rotate it independently — session logins are unaffected.

```bash
# Edit .env, replace BAMBOO_RELAY_SECRET (keep it different from the
# session secret), then roll BOTH so the controller (issuer) and the
# relay (verifier) pick up the new key together.
$EDITOR .env
docker compose up -d --force-recreate controller relay
```

Only relay tokens invalidate (TTL 1h); peers re-mint one automatically
on their next heartbeat, so there's no user-visible disruption.

## Capacity expectations

A single 4GB / 2 vCPU VPS handles:

- ~50 concurrent peer sessions
- ~500 daily active devices
- Postgres + ClickHouse comfortable headroom for 100k connection
  events / day

Beyond that, time to graduate to the multi-host AWS deployment.

## Known gaps

- **Postgres / ClickHouse / Caddy state is on the VPS volume.**
  Lose the VPS = lose the data. Set up automated backups (weekly
  pg_dump → R2 is the cheapest minimum).
- **No multi-relay failover.** This single relay is the only one;
  if it goes down, peers behind symmetric NAT lose their tunnel
  until restart. Acceptable for first-user dogfood.
- **OIDC redirect URIs are hard-coded to one DOMAIN.** If you change
  domain after registering OAuth, re-register the OAuth clients.
- **gRPC port (8080) is not exposed externally.** The Linux bamboo
  CLI uses gRPC; running it from outside the VPS requires `ssh -L
  8080:localhost:8080 vps`. Apple apps use REST and don't have this
  limitation.
