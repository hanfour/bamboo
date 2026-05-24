// SPDX-License-Identifier: AGPL-3.0-or-later

import type { PeerBandwidthSample } from './api';

// Bandwidth utilities for the §4 P2 PeerDrawer bandwidth section.
// The controller serves cumulative wg counters at heartbeat cadence
// (~30s); rendering meaningful throughput requires the consumer to
// compute deltas. Pure functions so a follow-up that adds vitest can
// pin them without React.

// BandwidthDelta is one interval between two adjacent cumulative
// samples — bytes transferred per direction and the elapsed seconds
// the transfer covered. ratePerSec is computed once here so the
// renderer doesn't divide on every render frame.
//
// We DROP samples where current.bytes < previous.bytes (interface
// reset / wg restart). The alternative (treating the negative as a
// fresh start and summing forward) would silently double-count the
// peer's lifetime traffic; better to render a small gap in the chart.
export type BandwidthDelta = {
  startedAt: Date;
  endedAt: Date;
  elapsedSec: number;
  sentBytes: number;
  receivedBytes: number;
  // Bytes/sec for each direction. Capped at the sample value when
  // elapsedSec is 0 (back-to-back samples in the same instant —
  // theoretically impossible per #187's bytes_sent ORDER BY tiebreak,
  // defensive in case the contract changes).
  sentBytesPerSec: number;
  receivedBytesPerSec: number;
};

export function computeDeltas(samples: PeerBandwidthSample[]): BandwidthDelta[] {
  if (samples.length < 2) return [];
  const out: BandwidthDelta[] = [];
  for (let i = 1; i < samples.length; i++) {
    const prev = samples[i - 1];
    const cur = samples[i];
    // Counter reset (peer reboot, interface restart) — drop. A
    // single negative delta would otherwise yank the chart axis
    // to a range where every other bar disappears.
    if (cur.bytesSent < prev.bytesSent || cur.bytesReceived < prev.bytesReceived) {
      continue;
    }
    const startedAt = new Date(prev.occurredAt);
    const endedAt = new Date(cur.occurredAt);
    const elapsedSec = Math.max(0, (endedAt.getTime() - startedAt.getTime()) / 1000);
    const sentBytes = Number(cur.bytesSent - prev.bytesSent);
    const receivedBytes = Number(cur.bytesReceived - prev.bytesReceived);
    out.push({
      startedAt,
      endedAt,
      elapsedSec,
      sentBytes,
      receivedBytes,
      sentBytesPerSec: elapsedSec > 0 ? sentBytes / elapsedSec : sentBytes,
      receivedBytesPerSec: elapsedSec > 0 ? receivedBytes / elapsedSec : receivedBytes,
    });
  }
  return out;
}

// formatBytes renders a byte count with binary-prefixed units (KiB,
// MiB, GiB). Picked over decimal because the rest of the wg / net
// world (wgctrl, ip-link, etc.) reports binary; matching their idiom
// avoids the "is 1MB 1e6 or 2^20?" confusion when admins cross-check.
//
// Negative values are rendered with a leading "-"; the caller is
// responsible for upstream filtering of counter resets — see
// computeDeltas which drops them at the source.
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes)) return '—';
  const sign = bytes < 0 ? '-' : '';
  const abs = Math.abs(bytes);
  if (abs < 1024) return `${sign}${Math.round(abs)} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let i = -1;
  let n = abs;
  do {
    n /= 1024;
    i++;
  } while (n >= 1024 && i < units.length - 1);
  // <10 ⇒ 1 decimal place ("1.4 MiB"); ≥10 ⇒ integer ("142 MiB").
  // Two decimals would look noisy at heartbeat-scale traffic which
  // is typically tens of MiB per interval.
  const fixed = n < 10 ? n.toFixed(1) : Math.round(n).toString();
  return `${sign}${fixed} ${units[i]}`;
}

// formatRate renders bytes/sec as "<rate>/s". Same binary-prefixed
// scaling as formatBytes so the two read consistently in the same
// section. A separate function (rather than formatBytes + "/s")
// because the unit suffix needs to live INSIDE the string so the
// caller can pass the result straight to JSX without concatenation
// noise.
export function formatRate(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return '0 B/s';
  return `${formatBytes(bytesPerSec)}/s`;
}

// sumWindow returns the total bytes transferred across the given
// delta slice. Used by the section header's "last hour" / "last 24h"
// summary. Returns 0 for an empty slice (matches "no data" UI).
export function sumWindow(deltas: BandwidthDelta[]): { sent: number; received: number } {
  let sent = 0;
  let received = 0;
  for (const d of deltas) {
    sent += d.sentBytes;
    received += d.receivedBytes;
  }
  return { sent, received };
}

// sparklinePath converts a delta sequence into an SVG <path d="...">
// string for the inline throughput sparkline. The path is a polyline
// — straight segments rather than smoothed curves so spikes look
// like spikes (admins should see anomalies, not pretty splines).
//
// values is the per-tick rate (bytesPerSec) for either tx or rx.
// width / height define the SVG viewBox space; the path is auto-
// scaled to fill the height with the max value. A flat-zero sequence
// renders as a horizontal line at the bottom.
//
// Returns null when there's nothing to draw (≤1 value). The caller
// renders an empty-state message instead.
export function sparklinePath(values: number[], width: number, height: number): string | null {
  if (values.length < 2) return null;
  const max = Math.max(...values, 1); // 1 → flat line at bottom
  const stepX = width / (values.length - 1);
  const points = values.map((v, i) => {
    const x = i * stepX;
    const y = height - (v / max) * height;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  return `M ${points.join(' L ')}`;
}
