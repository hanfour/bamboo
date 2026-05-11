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
  createdAt: string; // ISO timestamp
  lastSeenAt?: string; // ISO timestamp
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
