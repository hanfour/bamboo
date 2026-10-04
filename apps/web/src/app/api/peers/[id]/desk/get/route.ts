// SPDX-License-Identifier: AGPL-3.0-or-later

import { type NextRequest } from 'next/server';
import { CONTROLLER_BASE, controllerHeaders } from '@/lib/controllerProxy';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(request: NextRequest, context: { params: { id: string } }) {
  const path = request.nextUrl.searchParams.get('path') ?? '';
  if (path.trim() === '') {
    return Response.json({ error: 'path is required' }, { status: 400 });
  }
  const url = new URL(`/api/v1/peers/${encodeURIComponent(context.params.id)}/desk/get`, CONTROLLER_BASE);
  url.searchParams.set('path', path);
  const upstream = await fetch(url, {
    headers: await controllerHeaders(),
    cache: 'no-store',
  });
  if (!upstream.ok) {
    const detail = await upstream.text();
    return new Response(detail, {
      status: upstream.status,
      headers: { 'Content-Type': upstream.headers.get('Content-Type') ?? 'text/plain; charset=utf-8' },
    });
  }
  const headers = new Headers();
  headers.set('Content-Type', upstream.headers.get('Content-Type') ?? 'application/octet-stream');
  const disposition = upstream.headers.get('Content-Disposition');
  if (disposition) headers.set('Content-Disposition', disposition);
  return new Response(upstream.body, { status: 200, headers });
}
