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
};

// FetchResult is a tri-state discriminated union for read paths
// where "couldn't reach the controller" needs to be distinguished
// from "controller said 404". The previous `T | null` pattern
// collapsed both into the same UI state ("not found"), which lies
// to the user when the real problem is a network outage.
//
// 'notFound' covers: missing id, bad uuid, peer in another tenant
// (all three intentionally indistinguishable on the wire per the
// cross-tenant probe-protection contract).
// 'error' covers: network failure, controller 5xx, malformed JSON.
export type FetchResult<T> =
  | { kind: 'ok'; value: T }
  | { kind: 'notFound' }
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
