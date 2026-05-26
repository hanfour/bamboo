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
  APIToken,
  DNSConfig,
  FetchResult,
  Invitation,
  LogEvent,
  Peer,
  PeerEvent,
  PeersResponse,
  PolicyHistoryRow,
  PreAuthKey,
  User,
  Webhook,
} from './types';

const BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';
const TENANT = process.env.BAMBOO_TENANT ?? 'default';
const SESSION_COOKIE = 'bamboo_session';

type ApiPeer = {
  id: string;
  tenantId: string;
  hostname: string;
  // Controller serializes the column as `peerDnsName`. Absent
  // (undefined) when the peer row's value is NULL — legacy rows
  // that haven't re-registered since the column landed in
  // migration 00008.
  peerDnsName?: string | null;
  ip: string;
  tags: string[];
  os: string;
  clientVersion: string;
  // upgradeAvailable: controller sets this true when clientVersion is
  // strictly behind the release-feed's latest tag. Absent on pre-#226
  // controllers and when the peer is up-to-date / feed disabled.
  // Wire shape is `true | absent` (omitempty on false) — never null.
  upgradeAvailable?: boolean;
  status: 'online' | 'offline' | 'disabled';
  wireguardPublicKey?: string;
  endpoints?: string[];
  wgEndpoint?: string;
  rxBytes?: number;
  txBytes?: number;
  createdAt: string;
  lastSeenAt?: string;
  lastHandshakeAt?: string;
  ownerEmail?: string;
  ownerDisplayName?: string;
  // approvalStatus is missing from pre-#133 controller responses;
  // we normalize undefined → 'approved' in the mapper so the
  // Web UI doesn't have to special-case the migration boundary.
  approvalStatus?: 'pending' | 'approved' | 'rejected';
  approvedAt?: string;
  connectionPath?: 'direct' | 'relay' | 'unknown' | null;
  connectionPathAt?: string;
  connectionLatencyMs?: number;
  // Issue #136 / #137 advertise fields. Optional/missing for pre-
  // #149 controller deployments; the mapper backfills to []/false.
  advertisedRoutes?: string[] | null;
  approvedRoutes?: string[] | null;
  exitNodeCapable?: boolean | null;
  exitNodeApproved?: boolean | null;
  usingExitNodePeerId?: string | null;
};

function apiPeerToPeer(p: ApiPeer): Peer {
  return {
    id: p.id,
    tenantId: p.tenantId,
    hostname: p.hostname,
    // Normalize null → undefined so the Peer shape has a single
    // "absent" representation for downstream React code; otherwise
    // every check would need `peer.peerDnsName != null`.
    peerDnsName: p.peerDnsName ?? undefined,
    ip: p.ip,
    tags: p.tags ?? [],
    os: p.os,
    clientVersion: p.clientVersion,
    upgradeAvailable: p.upgradeAvailable,
    status: p.status,
    wireguardPublicKey: p.wireguardPublicKey,
    endpoints: p.endpoints ?? [],
    wgEndpoint: p.wgEndpoint,
    rxBytes: p.rxBytes ?? 0,
    txBytes: p.txBytes ?? 0,
    createdAt: p.createdAt,
    lastSeenAt: p.lastSeenAt,
    lastHandshakeAt: p.lastHandshakeAt,
    ownerEmail: p.ownerEmail,
    ownerDisplayName: p.ownerDisplayName,
    // approvalStatus default 'approved' handles the back-compat
    // case described above. Once the Web UI is pinned to a
    // post-#133 controller everywhere we deploy, this fallback can
    // be dropped.
    approvalStatus: p.approvalStatus ?? 'approved',
    approvedAt: p.approvedAt,
    connectionPath: p.connectionPath ?? undefined,
    connectionPathAt: p.connectionPathAt,
    connectionLatencyMs: p.connectionLatencyMs,
    advertisedRoutes: p.advertisedRoutes ?? [],
    approvedRoutes: p.approvedRoutes ?? [],
    exitNodeCapable: p.exitNodeCapable ?? false,
    exitNodeApproved: p.exitNodeApproved ?? false,
    usingExitNodePeerId: p.usingExitNodePeerId ?? undefined,
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
  issuedAt?: string;
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

export async function fetchPeers(): Promise<FetchResult<PeersResponse>> {
  // Wire shape: { peers: ApiPeer[], latestClientVersion?: string }.
  // The list-versus-detail asymmetry — only the list carries
  // latestClientVersion — keeps the per-peer GET free of a field
  // that's identical across every row of the same tenant snapshot.
  const r = await fetchResult<{ peers: ApiPeer[]; latestClientVersion?: string | null }>(
    '/api/v1/peers',
  );
  if (r.kind !== 'ok') return r;
  return {
    kind: 'ok',
    value: {
      peers: r.value.peers.map(apiPeerToPeer),
      latestClientVersion: r.value.latestClientVersion ?? undefined,
    },
  };
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

// fetchUsers reads the tenant's user list. Admin-only on the wire
// (the controller returns 403 for member role), so the page renders
// the forbidden variant of FetchErrorState for non-admins.
// fetchDNS reads the tenant's DNS settings. Member-readable on the
// wire (no admin gate), so the page only renders the unauthorized
// variant of FetchErrorState when there's no session at all.
export async function fetchDNS(): Promise<FetchResult<DNSConfig>> {
  return fetchResult<DNSConfig>('/api/v1/dns');
}

// fetchInvitations returns every invitation in the tenant (pending +
// history). Admin-only on the wire — 403 surfaces as forbidden.
// Token is never present in list rows (the controller has only the
// bcrypt hash); plaintext is shown once at mint time via the
// inviteUserAction return.
export async function fetchInvitations(): Promise<FetchResult<Invitation[]>> {
  const r = await fetchResult<{ invitations: Invitation[] }>('/api/v1/invitations');
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.invitations ?? [] };
}

// fetchLogs reads recent policy-evaluation traces for the tenant.
// Member-readable; dev environments without ClickHouse return an
// empty list and the page renders an empty-state.
export async function fetchLogs(opts: { sinceHours?: number; limit?: number } = {}): Promise<FetchResult<LogEvent[]>> {
  const params = new URLSearchParams();
  if (opts.sinceHours) params.set('sinceHours', String(opts.sinceHours));
  if (opts.limit) params.set('limit', String(opts.limit));
  const qs = params.toString();
  const r = await fetchResult<{ logs: LogEvent[] }>(`/api/v1/logs${qs ? `?${qs}` : ''}`);
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.logs ?? [] };
}

export async function fetchUsers(): Promise<FetchResult<User[]>> {
  const r = await fetchResult<{ users: User[] }>('/api/v1/users');
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.users ?? [] };
}

export async function fetchPreAuthKeys(): Promise<FetchResult<PreAuthKey[]>> {
  // PreAuthKey on the wire might be missing autoApprove when the
  // controller predates #133. Normalize undefined → false so
  // PreAuthKey's required field is always populated for downstream
  // components.
  type ApiPreAuthKey = Omit<PreAuthKey, 'autoApprove'> & {
    autoApprove?: boolean;
  };
  const r = await fetchResult<{ keys: ApiPreAuthKey[] }>('/api/v1/preauth-keys');
  if (r.kind !== 'ok') return r;
  const keys = (r.value.keys ?? []).map<PreAuthKey>((k) => ({
    ...k,
    autoApprove: k.autoApprove ?? false,
  }));
  return { kind: 'ok', value: keys };
}

// fetchAPITokens lists every API token row for the tenant (§4 P2).
// Admin-only on the wire; non-admin callers see 403 translated by
// FetchErrorState. The bcrypt hash never reaches the Web side —
// the APIToken type doesn't even carry it.
export async function fetchAPITokens(): Promise<FetchResult<APIToken[]>> {
  const r = await fetchResult<{ tokens: APIToken[] }>('/api/v1/api-tokens');
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.tokens ?? [] };
}

// fetchWebhooks lists every subscription for the tenant (§4 P2).
// Admin-only on the wire; non-admin callers see a 403 which the
// FetchErrorState surface translates into the "permission needed"
// view. Secret is never returned by the controller's list path —
// the Webhook type doesn't even carry it.
export async function fetchWebhooks(): Promise<FetchResult<Webhook[]>> {
  const r = await fetchResult<{ webhooks: Webhook[] }>('/api/v1/webhooks');
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.webhooks ?? [] };
}

// Relay is one row from /api/v1/admin/relays (super-admin only).
// Mirrors the controller's relayJSON shape including the §4 P2
// stage 1 health fields. lastHealthStatus is normalised server-
// side to one of "unknown" / "healthy" / "unhealthy" so the
// renderer doesn't have to special-case nil. lastHealthError is
// the short cause text the reaper recorded; empty when status is
// not 'unhealthy'.
export type RelayHealthStatus = 'unknown' | 'healthy' | 'unhealthy';

export type Relay = {
  id: string;
  region: string;
  hostname: string;
  port: number;
  publicKey: string;
  enabled: boolean;
  lastHealthStatus?: RelayHealthStatus;
  lastHealthError?: string;
  lastHealthCheckAt?: string;
};

// fetchRelays lists every enabled relay in the registry along
// with each one's most recent health-probe outcome (§4 P2 stage 1).
// The endpoint is admin-only on the wire; non-admin callers get a
// 403 which the FetchErrorState surface translates into the
// "permission needed" view. Includes unhealthy rows so admins can
// see + debug them — that's the whole point of the page.
export async function fetchRelays(): Promise<FetchResult<Relay[]>> {
  const r = await fetchResult<{ relays: Relay[] }>('/api/v1/admin/relays');
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.relays ?? [] };
}

// fetchPolicyRevisions backs the /acl Versions tab (issue #134).
// The endpoint is admin-only on the wire; non-admin callers see a
// 403 which the FetchErrorState surface translates into the
// "permission needed" view rather than rendering an empty list.
export async function fetchPolicyRevisions(): Promise<FetchResult<PolicyHistoryRow[]>> {
  const r = await fetchResult<{ revisions: PolicyHistoryRow[] }>(
    '/api/v1/policy/revisions',
  );
  if (r.kind !== 'ok') return r;
  return { kind: 'ok', value: r.value.revisions ?? [] };
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

// PeerConnectionEvent is one path_change row from the #138 v2
// timeline. Mirrors the controller's apiPeerConnectionEventJSON in
// api.go — kept here so PeerDrawer doesn't need to reach into the
// raw API shape.
//
// path / prevPath are narrowed to the three values the controller's
// CHECK constraint on peers.connection_path permits. Kept as a named
// type rather than an inline `| string` union so callers can switch
// over it without TypeScript widening to plain string.
export type PeerConnectionPath = 'direct' | 'relay' | 'unknown';

export type PeerConnectionEvent = {
  id: string;
  occurredAt: string;
  eventType: string;
  path: PeerConnectionPath;
  prevPath?: PeerConnectionPath;
  rttMs?: number;
};

// fetchPeerConnectionEvents pulls the recent path-transition timeline
// for a single peer (issue #138 v2). Side-channel from the drawer's
// primary render: on any error path (auth, network, CH outage) we
// return an empty list so the timeline section degrades to empty-
// state without taking the whole drawer down with it.
export async function fetchPeerConnectionEvents(
  id: string,
  limit = 50,
): Promise<PeerConnectionEvent[]> {
  const r = await fetchResult<{ events: PeerConnectionEvent[] }>(
    `/api/v1/peers/${encodeURIComponent(id)}/connection-events?limit=${encodeURIComponent(String(limit))}`,
  );
  if (r.kind !== 'ok') return [];
  return r.value.events ?? [];
}

// PeerRouteConflict is one (peer's CIDR ↔ other peer's CIDR) overlap
// surfaced by the §3a route-conflict detector. The Web UI renders
// these as inline warning badges next to the approved-routes
// checkboxes in PeerDrawer's AdvertiseSection.
export type PeerRouteConflictKind = 'duplicate' | 'contains' | 'contained_by';

export type PeerRouteConflict = {
  // The peer's own CIDR — matches one of peer.approvedRoutes.
  cidr: string;
  // Relationship from this peer's perspective.
  kind: PeerRouteConflictKind;
  // The other side of the overlap. otherHostname is what the badge
  // surfaces in the UI; otherPeerId could become an anchor in a v2.
  otherPeerId: string;
  otherHostname: string;
  otherCidr: string;
};

// PeerBandwidthSample is one cumulative-counter row from the
// §4 P2 bandwidth metering side-channel. Mirrors the controller's
// apiPeerBandwidthSampleJSON in api.go. Cumulative — readers
// compute deltas via lib/bandwidth.ts:computeDeltas. Counter reset
// on peer reboot is detected by current < previous and dropped.
//
// path correlates the throughput with the connection-path state at
// the time of the heartbeat (one of "direct" / "relay" / "" for the
// CLI which doesn't report path yet). Optional — empty path is fine
// for the dashboard's first-cut rendering.
export type PeerBandwidthSample = {
  occurredAt: string;
  path: string;
  bytesSent: number;
  bytesReceived: number;
};

// fetchPeerBandwidth pulls the recent cumulative byte-counter
// samples for one peer (§4 P2). Side-channel from the drawer's
// primary render: any error path (auth, network, CH outage)
// collapses to [] so the bandwidth section renders empty rather
// than crashing the drawer. The endpoint defaults to a 24h window
// server-side; the dashboard renders a sparkline + last-window totals
// from whatever lands.
export async function fetchPeerBandwidth(id: string): Promise<PeerBandwidthSample[]> {
  const r = await fetchResult<{ samples: PeerBandwidthSample[] }>(
    `/api/v1/peers/${encodeURIComponent(id)}/bandwidth`,
  );
  if (r.kind !== 'ok') return [];
  return r.value.samples ?? [];
}

// fetchPeerRouteConflicts pulls the list of approved-route conflicts
// affecting this peer (§3a follow-up). Side-channel from the drawer's
// primary render: any error path (auth, network, controller outage)
// collapses to [] so the warning row degrades to "no conflicts known"
// without taking the drawer down. Operator can still see the route
// list and click Approve / Rollback.
export async function fetchPeerRouteConflicts(id: string): Promise<PeerRouteConflict[]> {
  const r = await fetchResult<{ conflicts: PeerRouteConflict[] }>(
    `/api/v1/peers/${encodeURIComponent(id)}/route-conflicts`,
  );
  if (r.kind !== 'ok') return [];
  return r.value.conflicts ?? [];
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
