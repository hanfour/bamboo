// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import { ingressState, validateIngressLabel, validateIngressPort, type IngressService } from './ingress';

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
