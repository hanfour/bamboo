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

## 2. P0 — all shipped 2026-05-18 ✅

These were the gaps that made the brand promise (\"zero-trust mesh you can
demo to a customer\") not yet real. All four closed on 2026-05-18.

| # | Title | Shipped via |
|---|---|---|
| [#132](https://github.com/hanfour/bamboo/issues/132) | ACL real enforcement — controller pushes allowed_ips, client applies | Controller compile already existed; CLI already enforced. PR [#143](https://github.com/hanfour/bamboo/pull/143) wired the Apple client (Mac + iOS) so the last consumer respects controller-pushed AllowedIps. Stale-cache trap (watch events carry no AllowedIps) solved by a side `cachedAllowedIPs` map; policy refresh fires on heartbeat policyChanged + watch policyChanged + peer-added under enforcement. |
| [#133](https://github.com/hanfour/bamboo/issues/133) | Device approval queue — admin gates first registration | PR [#142](https://github.com/hanfour/bamboo/pull/142). Migration 00009: peers.approval_status + approved_at + approved_by_user_id; pre_auth_keys.auto_approve flag. Coordinator filters PeerAdded events so pending peers never appear to others. Web `PendingPeersList` component above PeerTable. Mint dialog gains \"auto-approve\" checkbox. |
| [#134](https://github.com/hanfour/bamboo/issues/134) | ACL HCL editor + validate + simulator + rollback | PRs [#144](https://github.com/hanfour/bamboo/pull/144) (backend) + [#146](https://github.com/hanfour/bamboo/pull/146) (Web). New endpoints: POST /policy/validate, /simulate, /rollback + GET /revisions. AclEditor rewritten as Source / Preview / Versions tabs. Simulator renders N×N allow/deny matrix. |
| [#135](https://github.com/hanfour/bamboo/issues/135) | Close production dev-fallback (REQUIRE_AUTH everywhere) | PR [#141](https://github.com/hanfour/bamboo/pull/141). Operational default flip — Go gate was already wired in main; this PR makes `BAMBOO_REQUIRE_AUTH=true` the prod compose default. Verified on bamboo.miilink.net via cold-curl probe (401 on bare-slug, 200 on /me). |

## 3. P1 — all shipped 2026-05-18 ✅

| # | Title | Shipped via |
|---|---|---|
| [#136](https://github.com/hanfour/bamboo/issues/136) | Subnet router | PR [#149](https://github.com/hanfour/bamboo/pull/149) (combined with #137). Migration 00011: peer.advertised_routes + approved_routes columns. Admin approves a subset via POST /peers/{id}/routes. ACL compiler merges approved_routes into other peers' allowed_ips. |
| [#137](https://github.com/hanfour/bamboo/issues/137) | Exit nodes | PR [#149](https://github.com/hanfour/bamboo/pull/149). Migration 00011: peer.exit_node_capable + exit_node_approved + using_exit_node_peer_id. POST /peers/{id}/exit-node admin sign-off. ACL compiler appends 0.0.0.0/0 + ::/0 when src is pinned to dst and dst is exit_node_approved. |
| [#138](https://github.com/hanfour/bamboo/issues/138) | Connection log + diagnostics | PR [#148](https://github.com/hanfour/bamboo/pull/148) — v1: peer.connection_path enum on heartbeat + Web ⚡/🔄 glyph on PeerTable. Rolling-window timeline deferred to P2. |
| [#139](https://github.com/hanfour/bamboo/issues/139) | Tag owners | PR [#147](https://github.com/hanfour/bamboo/pull/147). HCL grammar adds `tagOwners` map. Policy.CanAssignTag helper enforced on PATCH /peers/{id}/tags. Case-insensitive email compare. |

## 3a. P1 follow-ups (not blocking)

The wire is plumbed end-to-end on the controller; the client + UI pieces
below are the next visible wins but aren't required for the brand promise:

- **CLI / Apple `--advertise-routes` / `--advertise-exit-node` flags.** REST accepts the fields; clients need to surface them in their config UX. Without these clients can't actually advertise anything; subnet router + exit node require the client opt-in for end-to-end value.
- **Web admin pending-advertisements queue.** Today the PeerDrawer doesn't surface \"this peer wants to advertise X / claim exit-node role\". The REST endpoints + peerJSON fields are ready; the UI section lands in a follow-up.
- **Connection-log timeline UI** (#138 v2). Rolling-window storage of path transitions (1h / 7d) + per-peer timeline section in PeerDrawer. ClickHouse-backed.
- **Side-by-side diff in ACL editor.** The Versions tab's expandable source + the Source tab cover the common workflow; a true two-pane diff renderer is a follow-up if traffic justifies.
- **HCL syntax highlighting** in ACL editor textarea. Bundle cost (~300KB CodeMirror) isn't justified yet.
- **Group definitions** (`group:engineering = [...]`) in tagOwners. Today owner lists are flat emails; group expansion is a P2 follow-up.
- **Route conflict detection** (two peers advertising overlapping CIDRs). Admin can pick whichever; explicit warning is v2.

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
- **Mobile-app exit-node toggle** — #137 shipped on the controller side
  (2026-05-18); the iOS / macOS picker UI is the next visible piece
  (tracked under "P1 follow-ups" §3a above).

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

**Doc lifecycle.** This roadmap was authored 2026-05-17 with §2 + §3 as
the open P0 + P1 work; on 2026-05-18 every issue listed there closed (see
the shipped-via columns). The doc now serves as historical record of the
P0+P1 sprint. New roadmap work should land in a follow-up doc dated
forward; if a P2 item from §4 gets filed as an issue, link it inline
rather than restructuring §2/§3 (which are now read-only).

