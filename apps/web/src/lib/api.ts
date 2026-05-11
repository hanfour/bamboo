// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Server-side fetch helpers for the controller's /api/v1/* endpoints.
//
// All functions are intended to run in React Server Components: they
// hit the controller directly and return strongly-typed shapes. Errors
// are caught and rendered as empty state so a single 500 from the
// controller does not break the page.
//
// Auth precedence mirrors the controller's middleware (server/api.go):
//
//   1. bamboo_session cookie from the incoming Next.js request, passed
//      through to the controller. This is the production path once a
//      Google / GitHub login has happened.
//   2. X-Tenant-Slug header pinned to the BAMBOO_TENANT env var. This
//      is the dev fallback when no session cookie is present yet.

import { cookies } from 'next/headers';

import type { AclPolicy, AclRule, FetchResult, Peer, PeerEvent } from './types';

const BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';
const TENANT = process.env.BAMBOO_TENANT ?? 'default';
const SESSION_COOKIE = 'bamboo_session';

type ApiPeer = {
  id: string;
  tenantId: string;
  hostname: string;
  ip: string;
  tags: string[];
  os: string;
  clientVersion: string;
  status: 'online' | 'offline' | 'disabled';
  wireguardPublicKey?: string;
  endpoints?: string[];
  wgEndpoint?: string;
  rxBytes?: number;
  txBytes?: number;
  createdAt: string;
  lastSeenAt?: string;
  lastHandshakeAt?: string;
};

function apiPeerToPeer(p: ApiPeer): Peer {
  return {
    id: p.id,
    tenantId: p.tenantId,
    hostname: p.hostname,
    ip: p.ip,
    tags: p.tags ?? [],
    os: p.os,
    clientVersion: p.clientVersion,
    status: p.status,
    wireguardPublicKey: p.wireguardPublicKey,
    endpoints: p.endpoints ?? [],
    wgEndpoint: p.wgEndpoint,
    rxBytes: p.rxBytes ?? 0,
    txBytes: p.txBytes ?? 0,
    createdAt: p.createdAt,
    lastSeenAt: p.lastSeenAt,
    lastHandshakeAt: p.lastHandshakeAt,
  };
}

type ApiPolicyRule = {
  id: string;
  action: 'allow' | 'deny';
  description?: string;
  sources: string[];
  destinations: string[];
};

type ApiPolicy = {
  tenantId: string;
  revision: number;
  hclSource: string;
  updatedAt?: string;
  rules: ApiPolicyRule[];
};

type ApiRecommendation = {
  id: string;
  kind: string;
  summary: string;
  diff: string;
  evidence: string[];
  confidence: number;
  generatedAt: string;
};

type ApiOverview = {
  tenantId: string;
  totalPeers: number;
  onlinePeers: number;
  offlinePeers: number;
  policyRevision: number;
  recommendationCount: number;
};

type ApiMe = {
  authenticated: boolean;
  userId?: string;
  email?: string;
  displayName?: string;
  oidcProvider?: string;
  isAdmin?: boolean;
  tenantId: string;
  tenantSlug: string;
  expiresAt?: string;
};

async function buildHeaders(): Promise<Record<string, string>> {
  const headers: Record<string, string> = { 'X-Tenant-Slug': TENANT };
  // Forward the session cookie from the incoming Next.js request to the
  // controller so the controller's auth middleware sees the same JWT
  // the browser holds. cookies() is async in Next 15+; await is safe
  // in 14 too via the type cast.
  const store = await cookies();
  const session = store.get(SESSION_COOKIE);
  if (session?.value) {
    headers['Cookie'] = `${SESSION_COOKIE}=${session.value}`;
  }
  return headers;
}

async function get<T>(path: string, fallback: T): Promise<T> {
  try {
    const res = await fetch(`${BASE}${path}`, {
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok) return fallback;
    return (await res.json()) as T;
  } catch {
    return fallback;
  }
}

export async function fetchOverview(): Promise<ApiOverview> {
  return get<ApiOverview>('/api/v1/overview', {
    tenantId: '',
    totalPeers: 0,
    onlinePeers: 0,
    offlinePeers: 0,
    policyRevision: 0,
    recommendationCount: 0,
  });
}

export async function fetchMe(): Promise<ApiMe> {
  return get<ApiMe>('/api/v1/me', {
    authenticated: false,
    tenantId: '',
    tenantSlug: TENANT,
  });
}

export async function fetchPeers(): Promise<Peer[]> {
  const body = await get<{ peers: ApiPeer[] }>('/api/v1/peers', { peers: [] });
  return body.peers.map(apiPeerToPeer);
}

// fetchPeer returns a FetchResult so the drawer can distinguish
// "controller said 404" (peer doesn't exist or is in another
// tenant — both collapse to notFound by the probe-protection
// contract) from "couldn't reach the controller" (network /
// 5xx / malformed JSON). The latter renders an error state so
// the user knows a refresh might help, not a "peer was deleted"
// message that lies about the cause.
export async function fetchPeer(id: string): Promise<FetchResult<Peer>> {
  try {
    const res = await fetch(`${BASE}/api/v1/peers/${encodeURIComponent(id)}`, {
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (res.status === 404) {
      return { kind: 'notFound' };
    }
    if (!res.ok) {
      return { kind: 'error', message: `controller responded ${res.status}` };
    }
    const p = (await res.json()) as ApiPeer;
    return { kind: 'ok', value: apiPeerToPeer(p) };
  } catch (e) {
    return { kind: 'error', message: (e as Error).message };
  }
}

// fetchPeerEvents returns the audit timeline for one peer. Errors
// (network, 404, malformed JSON) collapse to an empty list so the
// drawer renders a clean empty state — the table + drawer's primary
// data already came from fetchPeer; the timeline is a side-channel
// nice-to-have, not load-bearing.
export async function fetchPeerEvents(id: string): Promise<PeerEvent[]> {
  try {
    const res = await fetch(`${BASE}/api/v1/peers/${encodeURIComponent(id)}/events`, {
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok) return [];
    const body = (await res.json()) as { events: PeerEvent[] };
    return body.events ?? [];
  } catch {
    return [];
  }
}

export async function fetchPolicy(): Promise<AclPolicy> {
  const body = await get<ApiPolicy>('/api/v1/policy', {
    tenantId: '',
    revision: 0,
    hclSource: '',
    rules: [],
  });
  const rules: AclRule[] = body.rules.map((r) => ({
    id: r.id,
    action: r.action,
    description: r.description,
    sources: r.sources,
    destinations: r.destinations,
  }));
  return {
    revision: body.revision,
    hclSource: body.hclSource,
    rules,
    updatedAt: body.updatedAt ?? new Date(0).toISOString(),
  };
}

export async function fetchRecommendations(): Promise<ApiRecommendation[]> {
  const body = await get<{ recommendations: ApiRecommendation[] }>(
    '/api/v1/recommendations',
    { recommendations: [] },
  );
  return body.recommendations;
}

export type { ApiMe, ApiOverview, ApiRecommendation };
