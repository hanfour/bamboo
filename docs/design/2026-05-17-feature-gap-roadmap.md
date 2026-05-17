# Feature gap roadmap (2026-05-17)

Supersedes the functional roadmap section (§3-§5) of
`docs/design/2026-05-web-roadmap.md`. The visual direction section (§1-§2) of
that doc is also obsolete — see PR #130 (warm-dark Linear-warm redesign) and
PR #131 (mobile RWD + hero pattern rollout) for the current visual baseline.

This doc captures the gap analysis run after the warm-dark UI redesign
landed: where bamboo sits today vs Tailscale / NetBird / Twingate /
Headscale, what's been filed as GitHub issues, and what's parked in the
backlog.

## 1. Status of the prior P0 list (2026-05-12 → 2026-05-17)

| Prior P0 item | State |
|---|---|
| Users page + role model | ✅ Shipped (PRs #76+ around invite + OIDC) |
| Machine row enhancements | ✅ Owner column + DNS expand shipped (PRs #115, #130). Version-upgrade indicator still missing — small follow-up |
| Settings page skeleton | ✅ Shipped (PR #95-ish) |
| Top-nav IA expansion (4 → 6 items) | ✅ Sidebar now carries 7 items including DNS |
| ACL real enforcement | ❌ Still UI-only → re-filed as **#132** |
| Close production dev-fallback | ⚠️ Partial (#60 admin claim for /preauth-keys) → re-filed as **#135** |

The prior P1 list (DNS page, ACL editor, logs page, subnet routes / exit
nodes) has shipped partially: DNS page exists; ACL editor and subnet routes
and exit nodes are still outstanding.

## 2. Current P0 — filed 2026-05-17

These are the gaps that make the brand promise (\"zero-trust mesh you can
demo to a customer\") not yet real. All four shipped as GitHub issues today.

| # | Title | Why P0 |
|---|---|---|
| [#132](https://github.com/hanfour/bamboo/issues/132) | ACL real enforcement — controller pushes allowed_ips, client applies | Without this, ACL is decoration. Every other competitor enforces |
| [#133](https://github.com/hanfour/bamboo/issues/133) | Device approval queue — admin gates first registration | Biggest competitor gap. Anyone with a pre-auth key joins the mesh today |
| [#134](https://github.com/hanfour/bamboo/issues/134) | ACL HCL editor + validate + diff + simulator + rollback | Makes #132 usable — no point enforcing rules admins can't edit |
| [#135](https://github.com/hanfour/bamboo/issues/135) | Close production dev-fallback (REQUIRE_AUTH everywhere) | Roadmap memory P0-2 — can't ship prod with bare-header fallback |

**Suggested order**: #135 (security cutover) → #133 (approval gate, depends
on auth) → #132 (enforcement, biggest piece) → #134 (editor, depends on
#132 having a compiler).

## 3. Current P1 — filed 2026-05-17

These differentiate bamboo from \"just another Headscale fork\" and are
table-stakes against Tailscale.

| # | Title | Notes |
|---|---|---|
| [#136](https://github.com/hanfour/bamboo/issues/136) | Subnet router — advertise routes from a peer | Depends hard on #132 (compiler) |
| [#137](https://github.com/hanfour/bamboo/issues/137) | Exit node selection — route public traffic via another peer | Soft depends on #132 + #133 |
| [#138](https://github.com/hanfour/bamboo/issues/138) | Connection log + diagnostics — \"why isn't this peer connecting?\" | Reuses ReportMetrics RPC |
| [#139](https://github.com/hanfour/bamboo/issues/139) | Tag owners — RBAC for who can assign / remove tags | Soft depends on #134 (HCL editor) |

## 4. P2 backlog — not yet filed as issues

Items deliberately not filed as separate issues yet — they're real but the
P0/P1 stack above must land first. File when prior issues close.

### Commercial / multi-tenant lifecycle

- **Webhooks** (`settings/webhooks`) — push events (peer.register,
  peer.approve, acl.update) to external endpoints. SaaS integration story.
- **API tokens UI** (`settings/api-keys`) — CI / script credentials with
  fine-grained scopes. Today only OIDC sessions work.
- **Invite expiry / auto-revoke** — current invitations have `expires_at`
  but no scheduler to clean them up.
- **Tenant billing / plan tier** — multi-tenant SaaS requires this; today
  we have a tenant table without commercial metadata.

### Observability / operational maturity

- **Service metrics** — Prometheus endpoint on controller + relay + ai.
  Today only ad-hoc logging exists.
- **Support bundle** — client `bamboo support-bundle` collects last N logs
  + wg state + interface info into a zip for support tickets.
- **Audit log retention + immutability** — append-only enforcement at the
  DB layer (currently soft).

### Network / scale

- **Multi-relay registry** — today a single relay endpoint per tenant;
  Tailscale has DERP with geographic failover.
- **Bandwidth metering per peer** — useful for both pricing and abuse
  detection. Reuses Connection log (#138) data path.
- **NAT64 / IPv6 dual-stack** — today IPv4 only.

### Compliance / enterprise

- **SOC 2 gap list** — audit log immutability, admin activity export,
  session policy controls.
- **Data deletion / GDPR right-to-erasure** — workflow + audit trail.

## 5. P3 backlog — strategic / aspirational

Out of immediate scope, captured for direction only.

- **AI policy recommendations adopted into core flow** — today `apps/ai`
  exists as a separate service emitting recommendations; surface them in
  the ACL editor (#134) as one-click \"accept this suggestion\".
- **Tier-2 anomaly detection** — connect-log → anomaly pipeline →
  controller. Depends on #138 + service metrics.
- **Apps integration** (OAuth SaaS apps locked to the tailnet) — e.g. an
  internal Notion only reachable from tailnet IPs. Different problem
  space from current ACL.
- **Per-app exit-node policy** (\"only Slack uses exit node\") — too
  complex for the v1 of #137.
- **Mobile-app exit-node toggle** — once #137 ships, iOS/macOS apps
  should expose the picker in-app.

## 6. Visual / UX small follow-ups

Not features, but visible-to-user polish items worth tracking:

- Version-upgrade indicator on PeerTable (\"this client is on 0.1.2,
  latest is 0.1.4\") — Tailscale shows this prominently.
- Users page + Logs page informative empty states (similar to DNS page
  empty state shipped in PR #130).
- ACL editor Examples tab (curated starter HCL: \"open mesh\", \"dev/prod
  isolation\", \"single exit node\") — Tailscale has this.
- Kebab menu row actions on PeerTable (disable, delete, expire) — today
  these only exist inside the drawer.
- Avatar dropdown in TopBar (replacing the inline email + sign-out chip).

## 7. Out of scope reaffirmed

- Custom per-tenant theming.
- Mobile-first redesign (mobile is responsive secondary surface; office
  Mac + admin laptop remains primary).
- Self-hosted relay as a product (relay is a tenant-shared resource we
  operate; users don't run it themselves on this product tier).

---

Update this doc rather than scattering rationale across PRs. When a P2
item gets filed as an issue, move it from §4 to §3 and add the issue link.
