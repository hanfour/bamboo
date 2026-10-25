// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import { buildGrid, computeStats, formatDuration, sumByDay } from './presence';

const entries = [
  { peerId: 'a', day: '2026-10-22', seconds: 3600 },
  { peerId: 'b', day: '2026-10-22', seconds: 1800 },
  { peerId: 'a', day: '2026-10-23', seconds: 7200 },
  { peerId: 'a', day: '2026-10-25', seconds: 600 },
  { peerId: 'a', day: '2026-08-01', seconds: 100 },
];

describe('sumByDay', () => {
  it('adds all peers, or filters to one', () => {
    expect(sumByDay(entries).get('2026-10-22')).toBe(5400);
    expect(sumByDay(entries, 'b').get('2026-10-22')).toBe(1800);
    expect(sumByDay(entries, 'b').get('2026-10-23')).toBeUndefined();
  });
});

describe('buildGrid', () => {
  it('ends on today and leaves later cells empty', () => {
    // 2026-10-25 is a Sunday: the last column holds only that day.
    const grid = buildGrid(sumByDay(entries), '2026-10-25');
    expect(grid).toHaveLength(53);
    const last = grid[52];
    expect(last[0]?.day).toBe('2026-10-25');
    expect(last.slice(1).every((c) => c === null)).toBe(true);
    expect(grid[0][0]?.day).toBe('2025-10-26');
  });

  it('scales levels to the busiest day', () => {
    const cells = buildGrid(sumByDay(entries), '2026-10-25').flat();
    const at = (d: string) => cells.find((c) => c?.day === d)?.level;
    expect(at('2026-10-23')).toBe(4);
    expect(at('2026-10-22')).toBe(3);
    expect(at('2026-10-24')).toBe(0);
  });
});

describe('computeStats', () => {
  it('finds best month and day and the streaks', () => {
    const s = computeStats(sumByDay(entries), '2026-10-25');
    expect(s.mostActiveMonth).toBe('2026-10');
    expect(s.mostActiveDay).toBe('2026-10-23');
    expect(s.longestStreak).toBe(2);
    expect(s.currentStreak).toBe(1);
    expect(s.totalSeconds).toBe(13300);
  });

  it('keeps the current streak when today is still idle', () => {
    const s = computeStats(sumByDay(entries), '2026-10-24');
    expect(s.currentStreak).toBe(2);
  });

  it('is empty without data', () => {
    const s = computeStats(new Map(), '2026-10-25');
    expect(s).toEqual({ totalSeconds: 0, mostActiveMonth: null, mostActiveDay: null, longestStreak: 0, currentStreak: 0 });
  });
});

describe('formatDuration', () => {
  it('uses minutes under an hour and hours above', () => {
    expect(formatDuration(1800)).toBe('30 m');
    expect(formatDuration(5400)).toBe('1.5 h');
    expect(formatDuration(400000)).toBe('111 h');
  });
});
