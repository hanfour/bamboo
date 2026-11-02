# Public services ingress (2026-11-02)

Status: design, not implemented.

## 1. Goal

Let a service running on a mesh peer be reachable from the public internet at
`https://<label>.svc.miilink.net`, for clients that are not mesh members.

The first user is YesChef on a Mac: hosted cloud agents (xAI, Anthropic, …)
call its MCP server over HTTPS. Those agents cannot run the bamboo client, so
the mesh alone does not reach them.

## 2. Decisions

- **TLS terminates on the operator's VPS.** The operator is the person running
  bamboo, not a third party. Caddy on the VPS terminates TLS and forwards plain
  HTTP to the peer over WireGuard (encrypted on the wire, including when the
  path falls back to a relay). This meets the consuming spec's baseline "no
  third party sees content". It does not hide content from the bamboo
  operator; if bamboo is later offered to other tenants, upgrade to SNI
  passthrough with peer-side certificates (see §9).
- **A real product feature**, not a hand-edited Caddyfile: services are
  configured in the controller, approved by an admin, audited, and wired into
  Caddy and the ACL automatically.
- **Domain:** `*.svc.miilink.net`. One certificate per name via Caddy on-demand
  TLS gated by the controller; no wildcard certificate.
- **The relay protocol is not touched.** Traffic rides the existing WireGuard
  mesh. The VPS joins the tenant as an ordinary Linux peer.

Accepted trade-offs of the subdomain choice: names inherit `miilink.net`'s HSTS
policy and share Let's Encrypt's 50 certificates/week/registered-domain limit
with the control plane. Cookies: services must not rely on cookies scoped to
`miilink.net`; the control plane's cookies stay host-only.

## 3. Data path

```
client ──https──> VPS :443  Caddy (TLS, on-demand cert, `ask` → controller)
                     └─ http ─> bamboo-ingress (host network, :8090 on the docker bridge gateway)
                                   └─ http over wg ─> <peer mesh IP>:<target_port>
```

- `bamboo-ingress` looks up `Host` → `(tenant, peer mesh IP, target_port)` and
  proxies with `httputil.ReverseProxy` (streaming, no buffering; supports SSE
  and chunked responses, which MCP Streamable HTTP uses).
- The VPS is a mesh peer (`bamboo up` + `install-service`, tagged
  `tag:ingress`) in the same tenant as the target peer. One ingress peer serves
  one tenant in this version.
- Requests carry `X-Forwarded-For`, `X-Forwarded-Proto: https`,
  `X-Forwarded-Host`, and `X-Bamboo-Ingress-Service: <service id>`. The target
  service must not trust these unless the connection comes from the ingress
  peer's mesh IP.

## 4. Control plane

### 4.1 Table `ingress_services`

| column | type | notes |
| --- | --- | --- |
| `id` | uuid | |
| `tenant_id` | uuid | RLS |
| `peer_id` | uuid | FK peers, cascade delete |
| `label` | text | `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`, globally unique across tenants |
| `target_port` | int | 1024–65535 |
| `enabled` | bool | default false |
| `approved_by` | uuid null | admin user id; null = pending |
| `approved_at`, `created_by`, `created_at`, `updated_at` | | |

Label uniqueness is cross-tenant, so the uniqueness check and the
`label → service` lookup used by ingress go through a `SECURITY DEFINER`
bootstrap function, following the RLS rules in
`docs/design/2026-09-23-project-goals.md` (P0) and ADR 0014. Reserved labels:
`www`, `api`, `admin`, `relay`, `controller`, `ingress`, `mail`.

A service is **live** when `enabled AND approved_by IS NOT NULL` and its peer
is approved and not deleted.

### 4.2 REST (admin)

Under the existing peer router (`apps/controller/internal/server/api_router.go`):

- `GET /api/v1/ingress` — list the tenant's services.
- `POST /api/v1/peers/{id}/ingress` — create `{label, target_port}` (pending).
- `POST /api/v1/ingress/{sid}/approve` — admin only.
- `PATCH /api/v1/ingress/{sid}` — `{enabled}`; changing `target_port` resets approval.
- `DELETE /api/v1/ingress/{sid}`.

Every mutation writes `audit_log` through `writePeerAuditTx`
(`apps/controller/internal/server/api.go`), actions `ingress.create`,
`ingress.approve`, `ingress.enable`, `ingress.disable`, `ingress.delete`.

### 4.3 Internal endpoints (ingress ↔ controller)

Authenticated with an Ed25519 service token in the same format as relay tokens
(`apps/controller/internal/auth/relay_token.go`), claims
`{tenant_id, peer_id (the ingress peer), role: "ingress", exp}`; issued from the
admin UI, stored on the VPS as a file.

- `GET /internal/v1/ingress/ask?domain=<fqdn>` — Caddy on-demand TLS `ask`.
  200 if `<label>.svc.miilink.net` is live (any tenant), else 404. Caddy's `ask`
  cannot send headers, so this endpoint takes no token; it answers only a yes/no
  for a name and is reachable only inside the compose network — the public
  Caddy site never proxies `/internal/*` (add an explicit `respond 404` matcher).
- `GET /internal/v1/ingress/routes` — `[{service_id, label, peer_ip, target_port}]`
  for live services in the tenant. ETag; ingress polls every 10 s and keeps the
  last good table when the controller is unreachable.

### 4.4 ACL

When a service becomes live, the controller adds a generated rule
`tag:ingress -> <peer>:<target_port>` (marked generated, not editable in the
HCL editor); removing liveness removes it. Enforcement is L3 only
(`apps/controller/internal/policy/enforce.go`), so the target service must
also restrict the source to the ingress peer's mesh IP. An admin-approved
public service's generated allow rules are placed before authored rules, so
they take precedence over an authored deny for the same pair; disable the
service to revoke.

- In a tenant with no ACL rules (full mesh), the ingress peer reaches only
  peers that host a live service, and no other peer reaches the ingress
  peer.
- Toward or through the ingress peer, AllowedIPs carry only the peer's own
  addresses (IPv4 /32 and IPv6 /128), never approved subnet routes, exit-node
  defaults, or the NAT64 prefix.
- In a tenant with rules, authored rules still apply to the ingress peer: an
  authored `* -> *:*` gives it the whole mesh (addresses only).

## 5. `bamboo-ingress` (new, `apps/ingress`, AGPLv3)

- Go binary on the host network (it must reach mesh IPs through the host's `wg` interface), listening on the docker bridge gateway address `:8090` so only the Caddy container can reach it. Config: controller URL, service
  token path, public suffix `svc.miilink.net`.
- Routing: exact `Host` match against the routes table; unknown host → 404,
  service gone → 502 with a short body.
- Limits (in-memory token bucket, reuse `apps/controller/internal/ratelimit`):
  per service 120 req/min, per client IP 300 req/min; request body ≤ 8 MiB
  (YesChef needs 5 MiB); upstream dial timeout 5 s; idle stream timeout 10 min.
- Telemetry: per request `{service_id, client_ip, status, bytes_in, bytes_out,
  duration}` to ClickHouse `connection_events` (`proto/bamboo/v1/telemetry.proto`);
  never request or response bodies, never `Authorization`.
- `/healthz`, `/version`, Prometheus `/metrics`.

## 6. Deployment (`infra/full`)

- DNS: `*.svc.miilink.net` A record → the Lightsail Tokyo static IP (one-time).
- Caddy: add a site block for `*.svc.miilink.net` with
  `tls { on_demand }`, global `on_demand_tls { ask http://controller:8081/internal/v1/ingress/ask }`
  (the controller's REST listener, `BAMBOO_HTTP_ADDR`), and
  `reverse_proxy host.docker.internal:8090` (compose `extra_hosts: host-gateway`) with
  `flush_interval -1`. Existing control-plane and relay sites are unchanged.
- The VPS's bamboo CLI peer runs as a systemd service (`install-service`); `bamboo-ingress` runs as a compose service with `network_mode: host` so it routes to mesh IPs through the `wg` interface.
- Never break the existing control plane: the change only adds a site block,
  a compose service, and a systemd unit.

## 7. Web UI

New page `apps/web/src/app/[locale]/ingress/page.tsx`: list services (label,
URL, peer, port, state), create on a peer, approve (admin), enable/disable,
delete, and issue/rotate the ingress service token. Follows the relays page
patterns.

## 8. Tests and acceptance

| # | What | How |
| --- | --- | --- |
| 1 | Migration up/down, RLS: tenant A cannot read/modify B's services; cross-tenant label uniqueness via bootstrap function | Go tests against Postgres (existing db test harness) |
| 2 | REST: create pending, approve (admin only), enable, port change resets approval, delete; audit rows written | handler tests |
| 3 | Internal endpoints: token auth, tenant scoping, `ask` 200/404, routes ETag | handler tests |
| 4 | ACL rule generated/removed with liveness | policy tests |
| 5 | Ingress: host routing, unknown host 404, limits (429), body cap (413), streaming SSE passes through unbuffered, controller outage keeps last table | Go tests with httptest upstream |
| 6 | Live: from the internet, `curl https://yeschef.svc.miilink.net/…` reaches the Mac through the VPS peer; disabling the service stops it within 10 s | manual on the Tokyo VPS |

## 9. Not in this version

- SNI passthrough with TLS terminating on the peer (hides content from the
  operator). Path when needed: a `bamboo-ingress` SNI front on :443, peer-side
  ACME TLS-ALPN-01, so the private key never leaves the peer.
- Multiple tenants per ingress host (needs per-tenant userspace WireGuard).
- User-supplied domains, TCP (non-HTTP) services, OAuth at the edge.
