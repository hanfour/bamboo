// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Server-side fetch helpers for the controller's /api/v1/* endpoints.
//
// All functions are intended to run in React Server Components: they
// hit the controller directly and return strongly-typed FetchResult
// shapes so pages can render the right state for each outcome (ok,
// notFound, unauthorized, forbidden, error). The legacy pattern of
// "collapse every failure into a fallback object" lies to the user
// when the cause is auth or a network outage — see lib/types.ts for
// the variants the renderer must handle.
//
// Auth precedence mirrors the controller's middleware (server/api.go):
//
//   1. bamboo_session cookie from the incoming Next.js request, passed
//      through to the controller. This is the production path once a
//      Google / GitHub login has happened.
//   2. X-Tenant-Slug header pinned to the BAMBOO_TENANT env var. This
//      is the dev fallback when no session cookie is present yet — the
//      controller honors it only when BAMBOO_REQUIRE_AUTH is unset.

import { cookies } from 'next/headers';

import type {
  AclPolicy,
  AclRule,
  ActivityEvent,
  FetchResult,
  Peer,
  PeerEvent,
  PreAuthKey,
} from './types';

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

// fetchResult is the canonical read-path helper: every fetcher in this
// file routes through it so the UI surface for auth + network errors
// is uniform. 401 / 403 / 404 / 5xx / network failure each map to a
// distinct FetchResult variant; the caller decides how to render.
async function fetchResult<T>(path: string): Promise<FetchResult<T>> {
  try {
    const res = await fetch(`${BASE}${path}`, {
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (res.status === 401) return { kind: 'unauthorized' };
    if (res.status === 403) return { kind: 'forbidden' };
    if (res.status === 404) return { kind: 'notFound' };
    if (!res.ok) {
      return { kind: 'error', message: `controller responded ${res.status}` };
    }
    const value = (await res.json()) as T;
    return { kind: 'ok', value };
  } catch (e) {
    return { kind: 'error', message: (e as Error).message };
  }
}

export async function fetchOverview(): Promise<FetchResult<ApiOverview>> {
  return fetchResult<ApiOverview>('/api/v1/overview');
}

// fetchMe deliberately stays "shape-stable" because the header reads
// it for every page render and treats every failure as "unauthenticated".
// Internally it still uses fetchResult so a 5xx surfaces in the log,
// but the public return type collapses to ApiMe with authenticated=false.
export async function fetchMe(): Promise<ApiMe> {
  const r = await fetchResult<ApiMe>('/api/v1/me');
  if (r.kind === 'ok') return r.value;
  return { authenticated: false, tenantId: '', tenantSlug: TENANT };
}

export async function fetchPeers(): Promise<FetchResult<Peer[]>> {
  const r = await fetchResult<{ peers: ApiPeer[] }>('/api/v1/peers');
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.peers.map(apiPeerToPeer) };
}

// fetchPeer is the per-id read used by the drawer; same variants as
// the list paths, with notFound carrying its existing UX meaning
// (probe-protection: peer in another tenant looks identical to a
// truly missing one).
export async function fetchPeer(id: string): Promise<FetchResult<Peer>> {
  const r = await fetchResult<ApiPeer>(`/api/v1/peers/${encodeURIComponent(id)}`);
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: apiPeerToPeer(r.value) };
}

export async function fetchPreAuthKeys(): Promise<FetchResult<PreAuthKey[]>> {
  const r = await fetchResult<{ keys: PreAuthKey[] }>('/api/v1/preauth-keys');
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.keys ?? [] };
}

export async function fetchActivity(limit = 20): Promise<FetchResult<ActivityEvent[]>> {
  const r = await fetchResult<{ events: ActivityEvent[] }>(
    `/api/v1/activity?limit=${encodeURIComponent(String(limit))}`,
  );
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.events ?? [] };
}

// fetchPeerEvents is a side-channel for the drawer's timeline tab —
// it does NOT block the drawer's primary render (which comes from
// fetchPeer). On error we collapse to an empty list so the timeline
// renders an empty-state without breaking the drawer; the primary
// peer fetch is what surfaces auth / network problems to the user.
export async function fetchPeerEvents(id: string): Promise<PeerEvent[]> {
  const r = await fetchResult<{ events: PeerEvent[] }>(
    `/api/v1/peers/${encodeURIComponent(id)}/events`,
  );
  if (r.kind !== 'ok') return [];
  return r.value.events ?? [];
}

export async function fetchPolicy(): Promise<FetchResult<AclPolicy>> {
  const r = await fetchResult<ApiPolicy>('/api/v1/policy');
  if (r.kind !== 'ok') return r;
  const rules: AclRule[] = r.value.rules.map((rule) => ({
    id: rule.id,
    action: rule.action,
    description: rule.description,
    sources: rule.sources,
    destinations: rule.destinations,
  }));
  return {
    kind: 'ok',
    value: {
      revision: r.value.revision,
      hclSource: r.value.hclSource,
      rules,
      updatedAt: r.value.updatedAt ?? new Date(0).toISOString(),
    },
  };
}

export async function fetchRecommendations(): Promise<FetchResult<ApiRecommendation[]>> {
  const r = await fetchResult<{ recommendations: ApiRecommendation[] }>(
    '/api/v1/recommendations',
  );
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.recommendations ?? [] };
}

export type { ApiMe, ApiOverview, ApiRecommendation };
