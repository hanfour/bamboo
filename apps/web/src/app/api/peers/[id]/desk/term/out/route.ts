// SPDX-License-Identifier: AGPL-3.0-or-later

import { type NextRequest } from 'next/server';
import { CONTROLLER_BASE, controllerHeaders } from '@/lib/controllerProxy';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

// The browser's EventSource cannot send the session cookie to the
// controller's origin, so this route proxies the terminal stream.
export async function GET(request: NextRequest, context: { params: { id: string } }) {
  const op = request.nextUrl.searchParams.get('op') ?? '';
  const url = new URL(`/api/v1/peers/${encodeURIComponent(context.params.id)}/desk/term/out`, CONTROLLER_BASE);
  if (op) url.searchParams.set('op', op);
  const upstream = await fetch(url, {
    headers: await controllerHeaders(),
    cache: 'no-store',
  });
  const headers = new Headers();
  headers.set('Content-Type', upstream.headers.get('Content-Type') ?? 'text/event-stream');
  headers.set('Cache-Control', 'no-cache');
  return new Response(upstream.body, { status: upstream.status, headers });
}
