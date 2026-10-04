// SPDX-License-Identifier: AGPL-3.0-or-later

import { type NextRequest } from 'next/server';
import { CONTROLLER_BASE, controllerHeaders } from '@/lib/controllerProxy';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const MAX_FILE = 4 * 1024 * 1024;

export async function POST(request: NextRequest, context: { params: { id: string } }) {
  const path = request.nextUrl.searchParams.get('path') ?? '';
  if (path.trim() === '') {
    return Response.json({ error: 'path is required' }, { status: 400 });
  }
  const body = await request.arrayBuffer();
  if (body.byteLength > MAX_FILE) {
    return Response.json({ error: 'file is larger than 4MiB' }, { status: 400 });
  }
  const url = new URL(`/api/v1/peers/${encodeURIComponent(context.params.id)}/desk/put`, CONTROLLER_BASE);
  url.searchParams.set('path', path);
  const headers = await controllerHeaders();
  headers['Content-Type'] = 'application/octet-stream';
  const upstream = await fetch(url, {
    method: 'POST',
    headers,
    body,
    cache: 'no-store',
  });
  const detail = await upstream.text();
  return new Response(detail, {
    status: upstream.status,
    headers: { 'Content-Type': upstream.headers.get('Content-Type') ?? 'application/json' },
  });
}
