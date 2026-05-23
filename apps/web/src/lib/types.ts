// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Public domain types matching the bamboo.v1 proto definitions.
// These will be replaced by generated TypeScript bindings once the
// proto pipeline emits them; for now they mirror the shapes used by
// the controller's gRPC services.

export type PeerStatus = 'online' | 'offline' | 'disabled';

export type Peer = {
  id: string;
  tenantId: string;
  hostname: string;
  // peerDnsName is the DNS-safe label resolvable as
  // `<peerDnsName>.bamboo` inside the tenant tailnet. Auto-slugged
  // by the controller on first register from `hostname`; admin can
  // rename via PATCH /api/v1/peers/{id}. Absent for legacy peers
  // that registered before MagicDNS landed and haven't re-
  // registered yet.
  peerDnsName?: string;
  ip: string;
  tags: string[];
  os: string;
  clientVersion: string;
  status: PeerStatus;
  wireguardPublicKey?: string;
  endpoints: string[];
  // wgEndpoint is the host:port the hub currently sees this peer
  // dial from (written by the wg-state reporter). Absent until the
  // reporter observes a non-"(none)" endpoint. Distinct from
  // `endpoints`, which is what the peer itself advertises.
  wgEndpoint?: string;
  rxBytes: number;
  txBytes: number;
  createdAt: string; // ISO timestamp
  lastSeenAt?: string; // ISO timestamp
  // lastHandshakeAt is strictly the WG handshake timestamp from
  // the reporter; absent = the peer has never handshook (UI shows
  // "尚未握手").
  lastHandshakeAt?: string;
  // ownerEmail / ownerDisplayName populated when the peer was
  // registered via a pre-auth key minted by a human admin. Absent
  // for legacy peers and dev-fallback registrations (no user
  // attribution available at register time).
  ownerEmail?: string;
  ownerDisplayName?: string;
  // approvalStatus is the device-approval lifecycle gate (issue
  // #133). One of:
  //   'pending'  — registered but not yet admin-approved. Hidden
  //                from other peers' mesh views; appears at the top
  //                of /peers in the admin-only PendingPeersList.
  //   'approved' — admin acted (or pre-auth-key carried
  //                auto_approve, or dev-fallback). Visible to other
  //                peers, participates in the mesh.
  //   'rejected' — admin denied. Retained for audit, pubkey unusable.
  // Defaults to 'approved' for legacy peers backfilled by migration
  // 00009 — clients should treat absent as 'approved' for back-
  // compat with pre-#133 controllers.
  approvalStatus: 'pending' | 'approved' | 'rejected';
  // approvedAt is the timestamp the admin (or auto-approve path)
  // flipped the gate. Backfilled to createdAt for legacy rows so
  // the UI timeline column stays coherent.
  approvedAt?: string;
  // connectionPath is how the peer is currently reaching the mesh
  // (issue #138). 'direct' ⇒ P2P WG handshake; 'relay' ⇒ NAT
  // traversal failed and the peer is using the relay fallback;
  // 'unknown' ⇒ legacy / pre-#138 row, or the peer hasn't reported
  // yet. The Web UI renders ⚡ direct / 🔄 relay icons next to the
  // online indicator on PeerTable.
  connectionPath?: 'direct' | 'relay' | 'unknown';
  connectionPathAt?: string;
  connectionLatencyMs?: number;
  // advertisedRoutes / approvedRoutes / exitNodeCapable /
  // exitNodeApproved / usingExitNodePeerId implement subnet routing
  // (issue #136) and exit nodes (issue #137). The peer reports what
  // it wants to advertise via the register body; the admin approves
  // a subset via POST /peers/{id}/routes (for routes) or POST
  // /peers/{id}/exit-node (for exit-node). Pre-#136/#137 controllers
  // omit these fields, in which case the mapper falls back to empty
  // arrays + false so the UI uniformly hides the advertise section.
  advertisedRoutes: string[];
  approvedRoutes: string[];
  exitNodeCapable: boolean;
  exitNodeApproved: boolean;
  // usingExitNodePeerId is the peer this row currently routes its
  // default traffic through (the chosen exit node). Absent when the
  // peer isn't using one — most common case.
  usingExitNodePeerId?: string;
};

// FetchResult is a discriminated union for read paths so the UI can
// distinguish "controller said 404", "you're not signed in", "you
// don't have permission", and "couldn't reach the controller" from
// each other. Collapsing every error into a single empty-state lies
// to the user when the real problem is auth or a network outage.
//
// Variants:
//   'notFound'      — 404: missing id, bad uuid, peer in another
//                     tenant (all three intentionally indistinguishable
//                     per the cross-tenant probe-protection contract).
//   'unauthorized'  — 401: no session cookie / invalid JWT. Render a
//                     sign-in prompt; do NOT auto-redirect (avoids
//                     redirect loops + lets the user keep their place).
//   'forbidden'     — 403: authenticated but missing role (today:
//                     not is_admin). Render a "needs admin" notice.
//   'error'         — network failure, controller 5xx, malformed JSON.
//                     Render a retry hint.
export type FetchResult<T> =
  | { kind: 'ok'; value: T }
  | { kind: 'notFound' }
  | { kind: 'unauthorized' }
  | { kind: 'forbidden' }
  | { kind: 'error'; message: string };

// PreAuthKey is one row from /api/v1/preauth-keys (list shape;
// the mint response is similar but includes the plaintext secret
// — see MintResult in lib/actions.ts). Status is derived in the
// renderer from revokedAt / expiresAt / useCount / reusable;
// keeping the controller agnostic of UI status enums lets the UI
// change presentation without a coordinated wire-shape bump.
export type PreAuthKey = {
  id: string;
  description?: string;
  reusable: boolean;
  ephemeral: boolean;
  // autoApprove, when true, makes peers minted with this key skip
  // the device-approval queue (issue #133). Defaults to false so
  // manual approval is the safer baseline.
  autoApprove: boolean;
  tags: string[];
  createdAt: string;
  expiresAt?: string;
  revokedAt?: string;
  useCount: number;
};

// Webhook is one subscription row from /api/v1/webhooks (list /
// patch / read shape — the create response carries an additional
// plaintext `secret` field, see WebhookCreateResult in lib/actions.ts).
// Secret is NEVER returned by list/patch reads; the controller
// shows it once at mint time and the UI must capture it then or
// rotate via delete-then-create.
export type Webhook = {
  id: string;
  url: string;
  eventTypes: string[];
  description?: string;
  active: boolean;
  createdAt: string;
  updatedAt: string;
};

// ActivityEvent is one row from the tenant-wide audit feed
// (`/api/v1/activity`). Same shape as PeerEvent but carries
// resourceType/resourceId because activity spans peer.* / policy.*
// / etc. The dashboard renders these to disambiguate which
// resource each event targets.
export type ActivityEvent = {
  id: string;
  actorType: 'user' | 'system' | 'api';
  actorId?: string;
  actorEmail?: string;
  action: string;
  resourceType?: string;
  resourceId?: string;
  diff?: Record<string, unknown>;
  occurredAt: string;
};

// PeerEvent is one row from the per-peer audit timeline. `diff` is
// the raw JSON the controller wrote when the action happened — for
// peer.update it's `{field: {from, to}}`, for peer.delete it's the
// pre-delete snapshot, for peer.register it's the registration body.
// The drawer renders the update shape specially and pretty-prints
// the rest.
export type PeerEvent = {
  id: string;
  actorType: 'user' | 'system' | 'api';
  actorId?: string;
  actorEmail?: string;
  action: string;
  diff?: Record<string, unknown>;
  occurredAt: string;
};

// PolicyHistoryRow is one row from GET /api/v1/policy/revisions
// (issue #134). The Web UI's Versions tab renders these with a
// rollback button; the HCL source is included inline so the user
// can diff the candidate revision against their current draft
// without a separate fetch round-trip per row.
export type PolicyHistoryRow = {
  revision: number;
  hclSource: string;
  appliedAt: string;
  // appliedByEmail is empty when the row was applied by a system
  // path (rollback from a dev-fallback session) or when the user
  // who applied it has since been deleted.
  appliedByEmail?: string;
};

// PolicySimulationEdge is one (src, dst, allow) triple from
// POST /api/v1/policy/simulate. The Preview tab renders the full
// edge list as a matrix so the operator can confirm a candidate
// policy does what they expect before they commit it.
export type PolicySimulationEdge = {
  sourceId: string;
  sourceHostname: string;
  destId: string;
  destHostname: string;
  allow: boolean;
  allowedIps?: string[];
};

export type PolicySimulation = {
  rules: number;
  edges: PolicySimulationEdge[];
};

export type AclRule = {
  id: string;
  action: 'allow' | 'deny';
  sources: string[];
  destinations: string[];
  timeWindow?: string;
  description?: string;
};

export type AclPolicy = {
  revision: number;
  hclSource: string;
  rules: AclRule[];
  updatedAt: string;
  updatedBy?: string;
};

// LogEvent is one row from /api/v1/logs — a policy-evaluation
// trace recorded by the controller when a peer attempted (or
// completed) a connection. Member-readable; the UI surfaces them
// in a Tailscale-style /admin/logs timeline.
export type LogEvent = {
  id: string;
  occurredAt: string;
  source: string;
  destination: string;
  port: number;
  protocol: string;
  action: 'allow' | 'deny';
  matchedRuleId?: string;
};

// User is one row from /api/v1/users (admin-only list). Role is
// derived in the UI from isAdmin — there's no third tier yet
// (owner / admin / member from the design doc are conceptual;
// currently only admin/member exist on the wire). updatedAt
// doubles as a "last activity" proxy since UpsertOIDC bumps it
// on every login.
export type User = {
  id: string;
  email: string;
  displayName?: string;
  oidcProvider?: string;
  isAdmin?: boolean;
  createdAt: string;
  updatedAt: string;
};

// DNSConfig is the tenant DNS surface from /api/v1/dns. tailnetName
// is a derived display field; the rest map 1:1 to tenant_dns_config
// columns. updatedAt = zero ISO string when no row has been written
// (defaults are surfaced). PUT is not yet implemented — the UI shows
// these as read-only with a "managed via API" hint.
export type DNSConfig = {
  tenantId: string;
  tenantSlug: string;
  tailnetName: string;
  magicDnsEnabled: boolean;
  globalNameservers: string[];
  searchDomains: string[];
  overrideDnsServers: boolean;
  updatedAt: string;
  updatedBy?: string;
};

// Invitation is one row from /api/v1/invitations (admin-only). The
// plaintext token is shown only on the mint response — list rows
// have only the bcrypt hash on the server, so this type doesn't
// carry it. Status (pending / accepted / revoked / expired) is
// derived in the renderer from acceptedAt / revokedAt / expiresAt;
// same idiom as PreAuthKey.
export type Invitation = {
  id: string;
  email: string;
  isAdmin: boolean;
  invitedBy?: string;
  createdAt: string;
  expiresAt: string;
  acceptedAt?: string;
  revokedAt?: string;
};
