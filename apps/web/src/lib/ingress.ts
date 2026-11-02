// SPDX-License-Identifier: AGPL-3.0-or-later

// Types and pure helpers for the public services ingress page
// (docs/design/2026-11-02-public-services-ingress.md §7). Mirrors the
// controller's ingressServiceJSON / ingressListJSON shapes.

import type { Peer } from './types';

export type IngressService = {
  id: string;
  peerId: string;
  peerHostname: string;
  peerIp: string;
  label: string;
  url: string; // "" when the controller has no suffix configured
  targetPort: number;
  enabled: boolean;
  approved: boolean;
  approvedAt?: string;
  live: boolean;
  aclRule?: string; // present only while live; generated, not editable
  createdAt: string;
  updatedAt: string;
};

export type IngressTokenInfo = { peerId: string; issuedAt: string; expiresAt: string };

export type IngressList = {
  services: IngressService[];
  suffix: string;
  token: IngressTokenInfo | null;
};

export type IngressState = 'pending' | 'approved' | 'live' | 'peerPending';

// Same rules as the controller (repo.ValidateIngressLabel); the
// controller stays the authority, this only gives instant feedback.
const LABEL_PATTERN = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;
const RESERVED = new Set(['www', 'api', 'admin', 'relay', 'controller', 'ingress', 'mail']);

export function validateIngressLabel(label: string): 'invalid' | 'reserved' | null {
  if (!LABEL_PATTERN.test(label)) return 'invalid';
  if (RESERVED.has(label)) return 'reserved';
  return null;
}

export function validateIngressPort(port: number): boolean {
  return Number.isInteger(port) && port >= 1024 && port <= 65535;
}

export function ingressState(s: IngressService): IngressState {
  if (s.live) return 'live';
  if (!s.approved) return 'pending';
  if (!s.enabled) return 'approved';
  return 'peerPending';
}

export type IngressReadiness = 'ready' | 'noIngressPeer' | 'noToken';

// ingressReadiness says whether a live service can actually be reached:
// the tenant needs an approved, enabled peer tagged ingress, and a token
// that has not expired (the controller's certificate check requires it).
export function ingressReadiness(
  peers: Pick<Peer, 'tags' | 'approvalStatus' | 'status'>[],
  token: IngressTokenInfo | null,
  now: Date = new Date(),
): IngressReadiness {
  const hasPeer = peers.some((p) => p.tags.includes('ingress') && p.approvalStatus === 'approved' && p.status !== 'disabled');
  if (!hasPeer) return 'noIngressPeer';
  const expires = token ? Date.parse(token.expiresAt) : Number.NaN;
  if (!(expires > now.getTime())) return 'noToken';
  return 'ready';
}

// formatIngressExpiry renders an RFC 3339 time in the viewer's locale,
// falling back to the raw value when it does not parse.
export function formatIngressExpiry(iso: string, locale: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(d);
}
