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
