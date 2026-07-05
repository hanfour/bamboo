// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import type { PeerBandwidthSample } from './api';
import {
  computeDeltas,
  formatBytes,
  formatRate,
  sumWindow,
  sparklinePath,
} from './bandwidth';

function sample(occurredAt: string, sent: number, received: number): PeerBandwidthSample {
  return { occurredAt, path: 'direct', bytesSent: sent, bytesReceived: received };
}

describe('computeDeltas', () => {
  it('returns [] for fewer than two samples', () => {
    expect(computeDeltas([])).toEqual([]);
    expect(computeDeltas([sample('2026-07-05T00:00:00Z', 0, 0)])).toEqual([]);
  });

  it('computes per-interval bytes and rate from cumulative counters', () => {
    const deltas = computeDeltas([
      sample('2026-07-05T00:00:00Z', 1000, 2000),
      sample('2026-07-05T00:00:10Z', 6000, 2000), // +5000 sent over 10s, +0 recv
    ]);
    expect(deltas).toHaveLength(1);
    expect(deltas[0].sentBytes).toBe(5000);
    expect(deltas[0].receivedBytes).toBe(0);
    expect(deltas[0].elapsedSec).toBe(10);
    expect(deltas[0].sentBytesPerSec).toBe(500);
  });

  it('drops an interval when either counter goes backwards (reset)', () => {
    const deltas = computeDeltas([
      sample('2026-07-05T00:00:00Z', 9000, 100),
      sample('2026-07-05T00:00:10Z', 100, 200), // sent reset 9000 -> 100
      sample('2026-07-05T00:00:20Z', 600, 700), // valid again: +500 / +500
    ]);
    // The reset interval is skipped; only the trailing valid one survives.
    expect(deltas).toHaveLength(1);
    expect(deltas[0].sentBytes).toBe(500);
    expect(deltas[0].receivedBytes).toBe(500);
  });

  it('falls back to raw bytes as the rate when elapsedSec is 0', () => {
    const deltas = computeDeltas([
      sample('2026-07-05T00:00:00Z', 0, 0),
      sample('2026-07-05T00:00:00Z', 300, 0), // same instant
    ]);
    expect(deltas[0].elapsedSec).toBe(0);
    expect(deltas[0].sentBytesPerSec).toBe(300); // not NaN / Infinity
  });
});

describe('formatBytes', () => {
  it('renders raw bytes below 1 KiB', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
  });

  it('scales with binary prefixes, 1 decimal below 10 and integer at/above', () => {
    expect(formatBytes(1536)).toBe('1.5 KiB'); // 1.5 * 1024
    expect(formatBytes(15 * 1024)).toBe('15 KiB'); // >=10 -> integer
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB');
    expect(formatBytes(3 * 1024 * 1024 * 1024)).toBe('3.0 GiB');
  });

  it('prefixes negatives with a minus and non-finite with an em dash', () => {
    expect(formatBytes(-2048)).toBe('-2.0 KiB');
    expect(formatBytes(Number.NaN)).toBe('—');
    expect(formatBytes(Number.POSITIVE_INFINITY)).toBe('—');
  });
});

describe('formatRate', () => {
  it('renders zero/negative/non-finite rates as "0 B/s"', () => {
    expect(formatRate(0)).toBe('0 B/s');
    expect(formatRate(-5)).toBe('0 B/s');
    expect(formatRate(Number.NaN)).toBe('0 B/s');
  });

  it('appends "/s" to a scaled byte count', () => {
    expect(formatRate(2048)).toBe('2.0 KiB/s');
  });
});

describe('sumWindow', () => {
  it('sums each direction and returns zeros for an empty slice', () => {
    expect(sumWindow([])).toEqual({ sent: 0, received: 0 });
    const deltas = computeDeltas([
      sample('2026-07-05T00:00:00Z', 0, 0),
      sample('2026-07-05T00:00:10Z', 100, 200),
      sample('2026-07-05T00:00:20Z', 400, 200),
    ]);
    expect(sumWindow(deltas)).toEqual({ sent: 400, received: 200 });
  });
});

describe('sparklinePath', () => {
  it('returns null when there is nothing to draw', () => {
    expect(sparklinePath([], 100, 20)).toBeNull();
    expect(sparklinePath([5], 100, 20)).toBeNull();
  });

  it('emits an M/L polyline scaled to the max value', () => {
    const d = sparklinePath([0, 10], 100, 20);
    // Two points: x at 0 and width; y inverted (0 -> bottom=height, max -> top=0).
    expect(d).toBe('M 0.0,20.0 L 100.0,0.0');
  });

  it('draws a flat line along the bottom for an all-zero series', () => {
    const d = sparklinePath([0, 0, 0], 100, 20);
    expect(d).toBe('M 0.0,20.0 L 50.0,20.0 L 100.0,20.0');
  });
});
