// @vitest-environment node
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shared label vectors (testdata/ingress-labels.json at the repository
// root), also read by the controller and bamboo-ingress tests, so the
// instant-feedback rules here cannot drift from the controller's.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { validateIngressLabel } from './ingress';

type Vectors = { labels: { valid: string[]; invalid: string[]; reserved: string[] } };

const vectors: Vectors = JSON.parse(
  readFileSync(fileURLToPath(new URL('../../../../testdata/ingress-labels.json', import.meta.url)), 'utf8'),
);

describe('validateIngressLabel (shared vectors)', () => {
  it('has vectors', () => {
    expect(vectors.labels.valid.length).toBeGreaterThan(0);
    expect(vectors.labels.invalid.length).toBeGreaterThan(0);
    expect(vectors.labels.reserved.length).toBeGreaterThan(0);
  });
  it.each(vectors.labels.valid)('accepts %j', (l) => expect(validateIngressLabel(l)).toBeNull());
  it.each(vectors.labels.invalid)('rejects %j as invalid', (l) => expect(validateIngressLabel(l)).toBe('invalid'));
  it.each(vectors.labels.reserved)('rejects %j as reserved', (l) => expect(validateIngressLabel(l)).toBe('reserved'));
});
