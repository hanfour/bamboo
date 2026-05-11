// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Server Actions for peer mutations. Each function runs on the
// Next.js server so the bamboo_session cookie + tenant headers can
// be attached to the controller request the same way fetchPeers
// does. On success we revalidate the /peers route so the drawer +
// table re-render with fresh server-rendered data on the next
// navigation tick.

'use server';

import { cookies } from 'next/headers';
import { revalidatePath } from 'next/cache';

const BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';
const TENANT = process.env.BAMBOO_TENANT ?? 'default';
const SESSION_COOKIE = 'bamboo_session';

export type ActionResult = { ok: true } | { ok: false; error: string };

async function buildHeaders(): Promise<Record<string, string>> {
  const headers: Record<string, string> = {
    'X-Tenant-Slug': TENANT,
    'Content-Type': 'application/json',
  };
  const store = await cookies();
  const session = store.get(SESSION_COOKIE);
  if (session?.value) {
    headers['Cookie'] = `${SESSION_COOKIE}=${session.value}`;
  }
  return headers;
}

async function patchPeer(
  id: string,
  body: Record<string, unknown>,
): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/peers/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: await buildHeaders(),
      body: JSON.stringify(body),
      cache: 'no-store',
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/peers', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

export async function renamePeerAction(id: string, hostname: string): Promise<ActionResult> {
  const trimmed = hostname.trim();
  if (trimmed === '') {
    return { ok: false, error: 'hostname cannot be empty' };
  }
  return patchPeer(id, { hostname: trimmed });
}

export async function setPeerStatusAction(
  id: string,
  status: 'online' | 'offline' | 'disabled',
): Promise<ActionResult> {
  return patchPeer(id, { status });
}

export async function setPeerTagsAction(id: string, tags: string[]): Promise<ActionResult> {
  return patchPeer(id, { tags });
}

export async function deletePeerAction(id: string): Promise<ActionResult> {
  try {
    const res = await fetch(`${BASE}/api/v1/peers/${encodeURIComponent(id)}`, {
      method: 'DELETE',
      headers: await buildHeaders(),
      cache: 'no-store',
    });
    if (!res.ok && res.status !== 204) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: `${res.status} ${text || res.statusText}` };
    }
    revalidatePath('/[locale]/peers', 'page');
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}
