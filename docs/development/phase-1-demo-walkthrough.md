# Phase 1 Demo Walkthrough

> **Note** — this walkthrough targets the AI-recommendations-on-the-Web
> dashboard demo from Phase 1. For the v0.1.4 ACL-enforcement-on-the-wire
> demo (10-minute script with proof outputs), use
> [`docs/demo.md`](../demo.md) instead. The two demos are complementary
> but the v0.1.4 one is the canonical answer to "show me zero-trust is
> real".

This is a step-by-step recipe for a reviewer (or future-you) to take a
clean clone of `hanfour/bamboo` and reach the point where the Web UI
shows all four AI recommendation kinds populated with real data:

- `KIND_UNUSED_RULE`        — Tier-1 rule-based: rule has not matched in N days
- `KIND_OVER_PRIVILEGED`    — Tier-1 rule-based: rule matched but only on a subset of its destinations
- `KIND_BROADEN_NEEDED`     — Tier-1 rule-based: legitimate flows are being denied that the rule could plausibly have allowed
- `KIND_FLAG_ANOMALOUS`     — Tier-2 ML: Isolation Forest flagged an outlier connection

The walkthrough assumes macOS or Linux. Anywhere a value is in `<angle
brackets>`, substitute your own.

## 0. Prerequisites

```bash
make doctor
```

Required: Go 1.22+, Docker (for Postgres + ClickHouse), Node 20+,
Python 3.11+. `make bootstrap` installs the missing Go tooling.

For Web UI auth you can stub or use real OIDC credentials. The demo
below uses the dev fallback (no OIDC login), which routes through the
`X-Tenant-Slug` header — the cookie path lights up once a real Google
or GitHub login completes against `/auth/google/login` or
`/auth/github/login`.

## 1. Bring up the dev stack

```bash
cd infra
docker compose up -d postgres redis
# ClickHouse is optional for Tier 1 but required for the anomaly card
docker compose up -d clickhouse
```

Apply schema:

```bash
cd ../apps/controller
go run ./cmd/controller migrate up
```

## 2. Start the controller

```bash
make build
./bin/controller serve --config apps/controller/config/example.yaml
```

The HTTP listener is on `:8081` (REST + OIDC), gRPC on `:8080`. Leave
this terminal running.

## 3. Register two peers via CLI

In a second terminal, mint a pre-auth key and run two `bamboo up`
sessions so the peer set has shape:

```bash
# Open a temporary admin session by hitting /auth/google/login in a
# browser, OR for the dev shortcut use grpcurl to the AuthService —
# either way you end up with a session JWT for tenant "default".

# Mint a reusable preauth key (admin):
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"reusable":true,"ttl_seconds":3600}' \
  localhost:8080 bamboo.v1.AuthService/CreatePreAuthKey
# → {"key":{"secret":"bka_..._..."}}

# Register peer 1:
./bin/bamboo --auth-key bka_xxx_yyy --hostname laptop-tw up --iface bamboo0

# Register peer 2 (separate machine or `--iface bamboo1` for a single host):
./bin/bamboo --auth-key bka_xxx_yyy --hostname server-jp up --iface bamboo1
```

## 4. Author an ACL with each shape

The recommendation engine needs rules that *can* fall into each kind.
Use `grpcurl` against `PolicyService.UpdatePolicy` with:

```hcl
acl "engineering" {
  description = "engineers reach the registry"
  action      = "allow"
  src         = ["tag:eng"]
  dst         = ["tag:registry:443", "tag:db:5432"]   # one of these will end up "over-privileged"
}

acl "stale" {
  description = "left over from a deprecated initiative"
  action      = "allow"
  src         = ["tag:legacy-bot"]
  dst         = ["10.20.0.0/16:*"]                    # nothing in the network has tag legacy-bot any more
}

acl "ops" {
  description = "ops jump host"
  action      = "allow"
  src         = ["tag:ops"]
  dst         = ["tag:bastion:22"]                    # legitimate flows to tag:bastion:80 will surface as "broaden"
}
```

Hand the HCL to the controller:

```bash
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d "$(jq -Rsn --arg src "$(cat acl.hcl)" '{hcl_source:$src}')" \
  localhost:8080 bamboo.v1.PolicyService/UpdatePolicy
```

## 5. Generate evaluation traffic

The recommendation engine reads from the `evaluation_traces` and
`connection_events` ClickHouse tables. Quickest path is to script a
loop that exercises each rule:

```bash
# Hit the registry repeatedly from peer 1 — establishes that the
# tag:registry:443 destination IS used (so tag:db:5432 will show as
# over-privileged)
for i in $(seq 1 200); do curl -s --interface 100.64.0.10 https://registry/health >/dev/null; done

# Try the bastion on port 80 from an ops peer — produces denied flows
# that look like they "should" be allowed (broaden)
for i in $(seq 1 50); do curl -s --interface 100.64.0.11 -m 1 http://bastion >/dev/null || true; done

# Do nothing for the legacy-bot rule — its absence of hits is what
# surfaces it as unused.
```

Wait ~10s for the controller's evaluation pipeline to flush traces
into ClickHouse.

## 6. Train + score the anomaly model

```bash
cd apps/ai
make ai-install        # if first time

# Train against the tenant's recent events:
.venv/bin/bamboo-ai train --tenant <tenant-uuid> --since 7d \
  --out /tmp/bamboo-anomaly.joblib

# Score and persist the top findings to ClickHouse:
.venv/bin/bamboo-ai score-and-write --tenant <tenant-uuid> \
  --model /tmp/bamboo-anomaly.joblib --threshold 0.6
```

The controller's `ListRecommendations` reads from
`anomaly_findings` directly, so once `score-and-write` finishes, the
KIND_FLAG_ANOMALOUS card will appear on the next page load.

## 7. Start the Web UI

```bash
cd apps/web
npm install
BAMBOO_API_URL=http://localhost:8081 BAMBOO_TENANT=default npm run dev
```

Open http://localhost:3000.

- **Header**: shows `Sign in` (links to `/auth/google/login` on the
  controller) when the cookie is absent. After completing Google or
  GitHub login, the same header re-renders showing
  `Signed in as <email>` + `Sign out`.
- **Dashboard**: peer counts, policy revision, recommendation count.
- **Peers**: the two registered peers with their IPs and last-seen.
- **ACL**: rules from step 4 with the source HCL viewable.
- **Recommendations**: four cards, one per kind. Each card shows
  summary + diff + evidence + confidence. The KIND_FLAG_ANOMALOUS
  card has a model-version badge ("isolation-forest-v1") so an
  operator can tell Tier-2 output from Tier-1.

## 8. Tear down

```bash
./bin/bamboo down --iface bamboo0
./bin/bamboo down --iface bamboo1
# Ctrl-C the controller and the web dev server
docker compose -f infra/docker-compose.yml down
```

## What this proves

End-to-end at the close of Phase 1, a single tenant can:

1. Authenticate via OIDC (Google / GitHub) **or** via a pre-auth key.
2. Register peers and reach each other over WireGuard.
3. Author an HCL ACL and have it evaluated on every connection.
4. See four kinds of AI recommendations driven by real telemetry —
   three from rule-based heuristics, one from a trained
   Isolation Forest scoring loop.
5. Drive every read above through the Web UI with locale switching
   (en / zh-TW) and an auth-aware header.

That set is the bar Phase 1 was scoped to clear.
