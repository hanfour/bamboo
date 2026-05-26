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
| Machine row enhancements | ✅ Owner column + DNS expand shipped (PRs #115, #130). Version-upgrade indicator shipped via PRs [#224](https://github.com/hanfour/bamboo/pull/224)–[#227](https://github.com/hanfour/bamboo/pull/227) (2026-05-26) |
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

## 3a. P1 follow-ups — status as of 2026-05-25

Most landed during the §4 P2 sprint. Updated marks:

- ✅ **CLI / Apple `--advertise-routes` / `--advertise-exit-node` flags.** CLI [#167](https://github.com/hanfour/bamboo/pull/167), Apple Settings fields [#168](https://github.com/hanfour/bamboo/pull/168). Subnet-router + exit-node verified end-to-end on prod 2026-05-22.
- ✅ **Web admin pending-advertisements queue** — PeerDrawer Advertise review section. [#169](https://github.com/hanfour/bamboo/pull/169).
- ✅ **Connection-log timeline UI** (#138 v2) — ClickHouse rolling window + Web ConnectionTimeline component. [#174](https://github.com/hanfour/bamboo/pull/174).
- ✅ **Side-by-side diff in ACL editor** — Versions tab gained a true two-pane diff (DIY LCS in `lib/lineDiff.ts`). [#175](https://github.com/hanfour/bamboo/pull/175).
- ❌ **HCL syntax highlighting** in ACL editor textarea. Bundle cost (~300KB CodeMirror) still not justified. Examples tab ([#213](https://github.com/hanfour/bamboo/pull/213)) addressed the "where do I start" friction instead.
- ✅ **Group definitions** in tagOwners — `groups = { "group:NAME" = [emails] }` block with lookup-time expansion. [#176](https://github.com/hanfour/bamboo/pull/176).
- ✅ **Route conflict detection** — internal/routes package + `GET /peers/{id}/route-conflicts` + amber warning badges in PeerDrawer AdvertiseSection (warn-only, doesn't block `POST /routes`). [#177](https://github.com/hanfour/bamboo/pull/177).

## 4. P2 backlog — status as of 2026-05-25

Originally deliberately not filed as separate issues. Most landed in the
2026-05 sprint; remaining items are larger initiatives that need their own
design phase rather than a single PR. ✅ marks delivered, ❌ marks
deferred to a future doc.

### Commercial / multi-tenant lifecycle

- ✅ **Webhooks** (`settings/webhooks`) — push events to external
  endpoints. Backend [#180](https://github.com/hanfour/bamboo/pull/180),
  Web [#181](https://github.com/hanfour/bamboo/pull/181).
- ✅ **API tokens UI** (`settings/api-tokens`) — CI / script credentials.
  Backend [#184](https://github.com/hanfour/bamboo/pull/184),
  Web [#185](https://github.com/hanfour/bamboo/pull/185).
- ✅ **Invite expiry / auto-revoke** — hourly reaper revokes expired
  invitations. [#186](https://github.com/hanfour/bamboo/pull/186).
- ❌ **Tenant billing / plan tier** — commercial SaaS scope; OSS defers.

### Observability / operational maturity

- ✅ **Service metrics** — Prometheus `/metrics` on controller; per-tenant
  gauges. [#178](https://github.com/hanfour/bamboo/pull/178) plus a
  per-tenant follow-up.
- ✅ **Support bundle** — `bamboo support-bundle` CLI subcommand collects
  logs + wg state + interface info into a redacted zip.
  [#183](https://github.com/hanfour/bamboo/pull/183).
- ✅ **Audit log immutability** — Postgres BEFORE UPDATE/DELETE triggers
  with a `bamboo.allow_audit_delete` session-var bypass for the retention
  reaper. [#182](https://github.com/hanfour/bamboo/pull/182).
- ✅ **Audit log retention** — hourly reaper deletes rows past
  `BAMBOO_AUDIT_RETENTION_DAYS` (default 365).
  [#205](https://github.com/hanfour/bamboo/pull/205).
- ✅ **Admin audit log CSV export** — `/api/v1/admin/audit-log.csv`
  streaming endpoint + Settings download card.
  Backend [#206](https://github.com/hanfour/bamboo/pull/206),
  Web [#207](https://github.com/hanfour/bamboo/pull/207).

### Network / scale

- ✅ **Multi-relay registry** — health-check reaper, RTT-based picker,
  RelaysChanged push event, web admin page with health badges,
  region-affinity hint, server-side region detection. Stages 1–5.5a
  across [#191](https://github.com/hanfour/bamboo/pull/191),
  [#192](https://github.com/hanfour/bamboo/pull/192),
  [#193](https://github.com/hanfour/bamboo/pull/193) /
  [#194](https://github.com/hanfour/bamboo/pull/194),
  [#195](https://github.com/hanfour/bamboo/pull/195) /
  [#196](https://github.com/hanfour/bamboo/pull/196) /
  [#197](https://github.com/hanfour/bamboo/pull/197),
  [#198](https://github.com/hanfour/bamboo/pull/198),
  [#199](https://github.com/hanfour/bamboo/pull/199),
  [#202](https://github.com/hanfour/bamboo/pull/202),
  with Apple mid-session relay swap on RelaysChanged
  [#204](https://github.com/hanfour/bamboo/pull/204).
- ✅ **Bandwidth metering per peer** — heartbeat carries rx/tx counters,
  per-peer drawer renders sparkline. Backend
  [#187](https://github.com/hanfour/bamboo/pull/187),
  CLI [#188](https://github.com/hanfour/bamboo/pull/188),
  Web [#189](https://github.com/hanfour/bamboo/pull/189),
  Apple [#190](https://github.com/hanfour/bamboo/pull/190).
- ❌ **NAT64 / IPv6 dual-stack** — needs IPv6 transit + NAT64 prefix +
  DNS64 design; cross-platform client work. Future sprint.

### Compliance / enterprise

- ✅ **SOC 2 gap list** — addressed by the audit chain above plus
  session-policy controls: slice 1 audit + TTL override
  [#210](https://github.com/hanfour/bamboo/pull/210),
  slice 2 Web Settings session card
  [#211](https://github.com/hanfour/bamboo/pull/211),
  slice 3a per-jti revocation
  [#212](https://github.com/hanfour/bamboo/pull/212),
  slice 3b admin force-sign-out
  [#218](https://github.com/hanfour/bamboo/pull/218) +
  Web button [#219](https://github.com/hanfour/bamboo/pull/219).
- ✅ **Data deletion / GDPR right-to-erasure** — hard-DELETE user row
  with cascade FKs handling dependents; audit row carries SHA-256 of
  email. Backend + Web button shipped together via
  [#208](https://github.com/hanfour/bamboo/pull/208) (which absorbed
  [#209](https://github.com/hanfour/bamboo/pull/209) as a stacked
  follow-up).

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

## 6. Visual / UX small follow-ups — status as of 2026-05-25

Not features, but visible-to-user polish items. Most landed in the same
sprint as §4.

- ✅ **Version-upgrade indicator on PeerTable** ("this client is on 0.1.2, latest is 0.1.4"). Shipped as a stack of four PRs (2026-05-26):
  [#224](https://github.com/hanfour/bamboo/pull/224) — `internal/releasefeed/` pkg (GitHub releases poller, nil-safe `*Feed`, 10-failure staleness ceiling, 1 MB body cap) + `internal/server/version_compare.go` (strict `<` semver via `golang.org/x/mod/semver`) + `ReleaseFeedConfig` (`*bool` Enabled for tri-state YAML/env, 5m interval floor);
  [#225](https://github.com/hanfour/bamboo/pull/225) — Apple `BundleVersion.swift` reads `CFBundleShortVersionString`, both `clientVersion: "0.0.1"` hard-codes in `ConnectionViewModel.swift` swap to `BundleVersion.current`;
  [#226](https://github.com/hanfour/bamboo/pull/226) — controller wires `*Feed` via `StartReleaseFeedPoller` (sibling of `StartAuditRetentionReaper` etc.); `apiPeers` embeds top-level `latestClientVersion` + per-peer `upgradeAvailable`;
  [#227](https://github.com/hanfour/bamboo/pull/227) — Web `PeerTable` "Client ver" column between OS and Status (`hidden lg:table-cell`), amber `↑ {latest}` for behind peers with SR `aria-label`.
  Verified locally: GitHub feed fetched `0.1.8` as latest; peer at `0.0.1` correctly flagged, peers at `0.1.10` (ahead) not flagged.
- ✅ **Users + Logs page informative empty states** — soft warm-bordered cards matching the DNS page vocabulary. [#216](https://github.com/hanfour/bamboo/pull/216).
- ✅ **ACL editor Examples tab** — curated starter HCL (open mesh, dev/prod isolation, single exit node). [#213](https://github.com/hanfour/bamboo/pull/213).
- ✅ **Kebab menu row actions on PeerTable** — Disable/Enable + Delete promoted from the drawer to the row. [#215](https://github.com/hanfour/bamboo/pull/215).
- ✅ **Avatar dropdown in TopBar** — collapsed the inline `email + sign-out` chip into a popover with Settings + Sign out. [#214](https://github.com/hanfour/bamboo/pull/214).

## 7. Out of scope reaffirmed

- Custom per-tenant theming.
- Mobile-first redesign (mobile is responsive secondary surface; office
  Mac + admin laptop remains primary).
- Self-hosted relay as a product (relay is a tenant-shared resource we
  operate; users don't run it themselves on this product tier).

---

**Doc lifecycle.** This roadmap was authored 2026-05-17 with §2 + §3 as
the open P0 + P1 work; on 2026-05-18 every issue listed there closed (see
the shipped-via columns).

A follow-up sprint (2026-05-19 → 2026-05-25) cleared the bulk of §3a,
§4 P2, and §6 polish — see the inline ✅ marks added on 2026-05-25.
Items still ❌ as of that date:

- §3a HCL syntax highlighting (CodeMirror bundle cost; Examples tab
  partially mitigates).
- §4 P2 NAT64 / IPv6 dual-stack.
- §4 P2 Tenant billing / plan tier (commercial scope).
- §6 PeerTable version-upgrade indicator (needs client version
  reporting plumbing first).

§5 P3 backlog is unchanged. New roadmap work should land in a follow-up
doc dated forward; this one now serves as historical record. If a
remaining backlog item gets filed as an issue, link it inline rather
than restructuring the section headings.

