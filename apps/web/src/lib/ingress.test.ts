// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import {
  formatIngressExpiry,
  ingressReadiness,
  ingressState,
  validateIngressLabel,
  validateIngressPort,
  type IngressService,
} from './ingress';
import type { Peer } from './types';

const base: IngressService = {
  id: 's1', peerId: 'p1', peerHostname: 'mac', peerIp: '100.64.0.5', label: 'yeschef',
  url: 'https://yeschef.svc.miilink.net', targetPort: 47800, enabled: false, approved: false,
  live: false, createdAt: '2026-11-02T00:00:00Z', updatedAt: '2026-11-02T00:00:00Z',
};

describe('validateIngressLabel', () => {
  it('accepts DNS labels up to 32 chars', () => {
    expect(validateIngressLabel('yeschef')).toBeNull();
    expect(validateIngressLabel('a-1')).toBeNull();
    expect(validateIngressLabel('a'.repeat(32))).toBeNull();
  });
  it('rejects bad shapes and reserved names', () => {
    for (const bad of ['', '-a', 'a-', 'A', 'a.b', 'a_b', 'a'.repeat(33)]) {
      expect(validateIngressLabel(bad)).toBe('invalid');
    }
    expect(validateIngressLabel('www')).toBe('reserved');
    expect(validateIngressLabel('ingress')).toBe('reserved');
  });
});

describe('validateIngressPort', () => {
  it('enforces 1024-65535', () => {
    expect(validateIngressPort(1024)).toBe(true);
    expect(validateIngressPort(65535)).toBe(true);
    expect(validateIngressPort(1023)).toBe(false);
    expect(validateIngressPort(65536)).toBe(false);
    expect(validateIngressPort(47800.5)).toBe(false);
  });
});

describe('ingressState', () => {
  it('maps the four states', () => {
    expect(ingressState(base)).toBe('pending');
    expect(ingressState({ ...base, approved: true })).toBe('approved');
    expect(ingressState({ ...base, approved: true, enabled: true, live: true })).toBe('live');
    expect(ingressState({ ...base, approved: true, enabled: true, live: false })).toBe('peerPending');
  });
});

describe('ingressReadiness', () => {
  const now = new Date('2026-11-02T00:00:00Z');
  const vps = { id: 'p2', tags: ['ingress'], approvalStatus: 'approved', status: 'online' } as unknown as Peer;
  const token = { peerId: 'p2', issuedAt: '2026-11-01T00:00:00Z', expiresAt: '2027-11-01T00:00:00Z' };
  it('is ready with an approved ingress peer and an unexpired token', () => {
    expect(ingressReadiness([vps], token, now)).toBe('ready');
  });
  it('needs an approved, enabled peer tagged ingress', () => {
    expect(ingressReadiness([], token, now)).toBe('noIngressPeer');
    expect(ingressReadiness([{ ...vps, tags: [] }], token, now)).toBe('noIngressPeer');
    expect(ingressReadiness([{ ...vps, approvalStatus: 'pending' } as Peer], token, now)).toBe('noIngressPeer');
    expect(ingressReadiness([{ ...vps, status: 'disabled' } as Peer], token, now)).toBe('noIngressPeer');
  });
  it('needs a token that has not expired', () => {
    expect(ingressReadiness([vps], null, now)).toBe('noToken');
    expect(ingressReadiness([vps], { ...token, expiresAt: '2026-11-01T00:00:00Z' }, now)).toBe('noToken');
  });
});

describe('formatIngressExpiry', () => {
  it('formats with the locale and falls back to the raw value', () => {
    const iso = '2027-11-02T00:00:00Z';
    expect(formatIngressExpiry(iso, 'en')).toBe(
      new Intl.DateTimeFormat('en', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso)),
    );
    expect(formatIngressExpiry(iso, 'zh-TW')).not.toBe(formatIngressExpiry(iso, 'en'));
    expect(formatIngressExpiry('not a date', 'en')).toBe('not a date');
  });
});
