// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import { parseTagList } from './tags';

describe('parseTagList', () => {
  it('splits on commas and trims', () => {
    expect(parseTagList(' db , web ')).toEqual(['db', 'web']);
  });

  it('drops a typed tag: prefix so the stored tag matches a tag: rule', () => {
    expect(parseTagList('tag:db, TAG:web')).toEqual(['db', 'web']);
  });

  it('drops empties and duplicates, keeping first-seen order', () => {
    expect(parseTagList('db,, web, db, tag:web')).toEqual(['db', 'web']);
  });

  it('returns an empty list for blank input', () => {
    expect(parseTagList('   ')).toEqual([]);
  });
});
