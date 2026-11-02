// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('next/cache', () => ({ revalidatePath: vi.fn() }));
vi.mock('@/lib/controllerProxy', () => ({
  CONTROLLER_BASE: 'http://ctl',
  controllerHeaders: async () => ({ Authorization: 'Bearer x' }),
}));

import { approveIngressAction, issueIngressTokenAction, createIngressAction } from './ingressActions';

afterEach(() => vi.unstubAllGlobals());

describe('ingress actions', () => {
  it('surfaces the controller 403 message', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('admin only', { status: 403 })));
    expect(await approveIngressAction('s1')).toEqual({ ok: false, error: '403 admin only' });
  });

  it('sends the token request body, never the token in the URL, and returns the token once', async () => {
    const f = vi.fn(async () => new Response(JSON.stringify({ token: 'tok', expiresAt: 'x' }), { status: 200 }));
    vi.stubGlobal('fetch', f);
    const res = await issueIngressTokenAction('p2');
    expect(res).toEqual({ ok: true, token: 'tok', expiresAt: 'x' });
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('http://ctl/api/v1/ingress/token');
    expect(init.body).toBe(JSON.stringify({ peer_id: 'p2' }));
  });

  it('normalizes the label on create', async () => {
    const f = vi.fn(async () => new Response('{}', { status: 201 }));
    vi.stubGlobal('fetch', f);
    await createIngressAction('p1', ' Notes ', 47800);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('http://ctl/api/v1/peers/p1/ingress');
    expect(init.body).toBe(JSON.stringify({ label: 'notes', target_port: 47800 }));
  });
});
