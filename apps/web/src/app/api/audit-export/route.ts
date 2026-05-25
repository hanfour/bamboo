// SPDX-License-Identifier: AGPL-3.0-or-later

import { cookies } from 'next/headers';
import { type NextRequest } from 'next/server';

// audit-export route handler proxies the browser's GET to the
// controller's /api/v1/admin/audit-log.csv (shipped in PR #206)
// with the session cookie forwarded. The browser can't hit the
// controller directly — different origin, cookies don't cross —
// so this Next-side proxy is the canonical pattern for any
// admin download that needs the user-session JWT.
//
// Why not a Server Action: actions return JSON-serialisable data
// to a client component; for binary / large downloads they would
// buffer in memory first. Streaming the controller's CSV through
// a Route Handler hands the body straight to the browser's
// download manager — no buffering, no size ceiling beyond what
// the controller already caps at (auditExportMaxRows = 50k).

const BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';
const TENANT = process.env.BAMBOO_TENANT ?? 'default';
const SESSION_COOKIE = 'bamboo_session';

export async function GET(request: NextRequest) {
  const url = new URL('/api/v1/admin/audit-log.csv', BASE);
  // Pass through the three query params the controller accepts
  // (since / until / limit). Anything else gets dropped — the
  // controller would 400 on unknown params, so we filter rather
  // than forward to make the failure surface here be "I didn't
  // recognise this filter" instead of an upstream 400.
  for (const key of ['since', 'until', 'limit']) {
    const v = request.nextUrl.searchParams.get(key);
    if (v) url.searchParams.set(key, v);
  }

  const store = await cookies();
  const session = store.get(SESSION_COOKIE);
  const headers: Record<string, string> = { 'X-Tenant-Slug': TENANT };
  if (session?.value) {
    headers['Cookie'] = `${SESSION_COOKIE}=${session.value}`;
  }

  const upstream = await fetch(url, { headers, cache: 'no-store' });
  if (!upstream.ok) {
    // Surface the controller's status + body for debugging. The
    // browser will see this in DevTools, not as a CSV download —
    // good enough for the "I clicked the button and got nothing"
    // failure mode.
    const detail = await upstream.text();
    return new Response(detail, {
      status: upstream.status,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    });
  }
  // Stream the body straight through. Forward the two headers the
  // controller sets that the browser needs to treat this as a
  // file download: Content-Type for the MIME, Content-Disposition
  // for the filename. Everything else (cache, security headers)
  // Next decides — this is admin-gated content so caching is
  // already wrong upstream of us.
  const out = new Headers();
  out.set('Content-Type', upstream.headers.get('Content-Type') ?? 'text/csv; charset=utf-8');
  const disposition = upstream.headers.get('Content-Disposition');
  if (disposition) out.set('Content-Disposition', disposition);
  return new Response(upstream.body, { status: 200, headers: out });
}
