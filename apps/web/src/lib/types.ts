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
  tags: string[];
  createdAt: string;
  expiresAt?: string;
  revokedAt?: string;
  useCount: number;
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
