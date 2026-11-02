// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Server Actions for the /ingress page. Same session forwarding as
// lib/actions.ts, via controllerProxy's exported helpers ('use server'
// files may only export async functions, so nothing is shared from
// actions.ts).

'use server';

import { revalidatePath } from 'next/cache';
import { CONTROLLER_BASE, controllerHeaders } from '@/lib/controllerProxy';

export type IngressActionResult = { ok: true } | { ok: false; error: string };
export type IngressTokenResult =
  | { ok: true; token: string; expiresAt: string }
  | { ok: false; error: string };

async function send(method: string, path: string, body?: unknown): Promise<Response> {
  const headers = await controllerHeaders();
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  return fetch(`${CONTROLLER_BASE}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: 'no-store',
  });
}

async function run(method: string, path: string, body?: unknown): Promise<IngressActionResult> {
  try {
    const res = await send(method, path, body);
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/ingress', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

export async function createIngressAction(peerId: string, label: string, targetPort: number): Promise<IngressActionResult> {
  return run('POST', `/api/v1/peers/${encodeURIComponent(peerId)}/ingress`, {
    label: label.trim().toLowerCase(),
    target_port: targetPort,
  });
}

export async function approveIngressAction(id: string): Promise<IngressActionResult> {
  return run('POST', `/api/v1/ingress/${encodeURIComponent(id)}/approve`);
}

export async function setIngressEnabledAction(id: string, enabled: boolean): Promise<IngressActionResult> {
  return run('PATCH', `/api/v1/ingress/${encodeURIComponent(id)}`, { enabled });
}

export async function deleteIngressAction(id: string): Promise<IngressActionResult> {
  return run('DELETE', `/api/v1/ingress/${encodeURIComponent(id)}`);
}

// issueIngressTokenAction returns the token once; it is never readable again.
export async function issueIngressTokenAction(peerId: string): Promise<IngressTokenResult> {
  try {
    const res = await send('POST', '/api/v1/ingress/token', { peer_id: peerId });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    const body = (await res.json()) as { token: string; expiresAt: string };
    revalidatePath('/[locale]/ingress', 'page');
    return { ok: true, token: body.token, expiresAt: body.expiresAt };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}
