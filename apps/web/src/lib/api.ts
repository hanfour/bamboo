// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Server-side fetch helpers for the controller's /api/v1/* endpoints.
//
// All functions are intended to run in React Server Components: they
// hit the controller directly with the X-Tenant-Slug header and return
// strongly-typed shapes. Errors are caught and rendered as empty
// state so a single 500 from the controller does not break the page.

import type { AclPolicy, AclRule, Peer } from './types';

const BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';
const TENANT = process.env.BAMBOO_TENANT ?? 'default';

type ApiPeer = {
  id: string;
  tenantId: string;
  hostname: string;
  ip: string;
  tags: string[];
  os: string;
  clientVersion: string;
  status: 'online' | 'offline' | 'disabled';
  lastSeenAt?: string;
};

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

async function get<T>(path: string, fallback: T): Promise<T> {
  try {
    const res = await fetch(`${BASE}${path}`, {
      headers: { 'X-Tenant-Slug': TENANT },
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

export async function fetchPeers(): Promise<Peer[]> {
  const body = await get<{ peers: ApiPeer[] }>('/api/v1/peers', { peers: [] });
  return body.peers.map((p) => ({
    id: p.id,
    tenantId: p.tenantId,
    hostname: p.hostname,
    ip: p.ip,
    tags: p.tags ?? [],
    os: p.os,
    clientVersion: p.clientVersion,
    status: p.status,
    lastSeenAt: p.lastSeenAt,
  }));
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

export type { ApiOverview, ApiRecommendation };
