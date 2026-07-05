// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import { splitLines, lineDiff } from './lineDiff';

describe('splitLines', () => {
  it('returns [] for the empty string (not [""])', () => {
    expect(splitLines('')).toEqual([]);
  });

  it('splits on newlines and strips a single trailing newline', () => {
    expect(splitLines('a\nb')).toEqual(['a', 'b']);
    expect(splitLines('a\nb\n')).toEqual(['a', 'b']);
  });

  it('normalizes CRLF to LF', () => {
    expect(splitLines('a\r\nb\r\n')).toEqual(['a', 'b']);
  });
});

describe('lineDiff', () => {
  it('marks every line "same" for identical inputs', () => {
    const rows = lineDiff('a\nb\nc', 'a\nb\nc');
    expect(rows.map((r) => r.kind)).toEqual(['same', 'same', 'same']);
    expect(rows[0]).toMatchObject({ oldLine: 'a', newLine: 'a', oldLineNumber: 1, newLineNumber: 1 });
  });

  it('emits all-add when the old side is empty', () => {
    const rows = lineDiff('', 'x\ny');
    expect(rows.map((r) => r.kind)).toEqual(['add', 'add']);
    expect(rows.map((r) => r.newLine)).toEqual(['x', 'y']);
    expect(rows.every((r) => r.oldLine === undefined)).toBe(true);
  });

  it('emits all-remove when the new side is empty', () => {
    const rows = lineDiff('x\ny', '');
    expect(rows.map((r) => r.kind)).toEqual(['remove', 'remove']);
    expect(rows.map((r) => r.oldLine)).toEqual(['x', 'y']);
  });

  it('represents a middle replacement as add + remove around unchanged context', () => {
    const rows = lineDiff('keep\nold\ntail', 'keep\nnew\ntail');
    // NOTE: the emitted order is add-BEFORE-remove, which contradicts
    // lineDiff.ts's own comment ("prefer removing ... groups removals
    // above additions"). The backward DP walk pushes the 'remove' first,
    // but the final out.reverse() flips it below the 'add'. Pinned here as
    // the actual behavior; whether the renderer should show removals above
    // additions is a separate rendering decision (follow-up).
    expect(rows.map((r) => r.kind)).toEqual(['same', 'add', 'remove', 'same']);
    const removed = rows.find((r) => r.kind === 'remove');
    const added = rows.find((r) => r.kind === 'add');
    expect(removed?.oldLine).toBe('old');
    expect(added?.newLine).toBe('new');
    // Context line numbers stay aligned to each side.
    expect(rows[0]).toMatchObject({ oldLineNumber: 1, newLineNumber: 1 });
    expect(rows[3]).toMatchObject({ oldLineNumber: 3, newLineNumber: 3 });
  });

  it('preserves the LCS of a pure insertion', () => {
    const rows = lineDiff('a\nc', 'a\nb\nc');
    expect(rows.map((r) => r.kind)).toEqual(['same', 'add', 'same']);
    expect(rows[1].newLine).toBe('b');
  });
});
