// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';
import type { Relay, RelayHealthStatus } from '@/lib/api';

// RelaysTable renders the §4 P2 stage 3 admin view of the relay
// registry. Read-only — operators add/remove via REST until the
// create form ships in a follow-up. The PRIMARY signal of the
// page is the health badge column: a relay flagged 'unhealthy'
// by the controller's stage 1 reaper renders in amber/red and
// reveals the underlying reason, so an operator on-call doesn't
// have to grep controller logs to learn "why is my relay down."
//
// Empty state matters: a fresh deployment has zero relays
// configured, and that's NOT an error — relays are optional
// infrastructure. The empty copy explains both that no relays
// are registered and that registration goes through the REST
// admin endpoint until the create form lands.
//
// Live refresh (§4 P2 stage 4c): the table re-fetches itself every
// relayRefreshIntervalMs via router.refresh(), so an admin watching
// the page sees badges flip in step with the controller's 30s
// health reaper without manual reload. The `nowTick` state separately
// re-renders the "Ns ago" timestamps on a faster cadence so a row
// stuck at "5m ago" reads as live-but-old rather than a frozen
// page. router.refresh() preserves client component state — focus,
// scroll position, any open inline editors — so the live refresh
// stays invisible to anyone not staring at the badges.
//
// Polling vs SSE: SSE would need a new admin-side bus stream + a
// privacy review of which events leak across tenants. Polling
// every 30s matches the reaper's own cadence (no flips happen
// faster), so the latency floor is the same with vastly less
// machinery. The SSE path is a noted follow-up when an admin
// actually needs sub-second relay visibility.
const relayRefreshIntervalMs = 30_000;
const relayClockTickMs = 5_000;

export function RelaysTable({ relays }: { relays: Relay[] }) {
  const t = useTranslations('relays');
  const router = useRouter();
  // Force a re-render every clockTickMs so the "Ns ago" text on
  // each row stays fresh between server refetches. We just need a
  // changing value; the actual number is unused, useState's setter
  // is what triggers the render.
  const [, setNow] = useState(0);
  useEffect(() => {
    const refresh = setInterval(() => router.refresh(), relayRefreshIntervalMs);
    const tick = setInterval(() => setNow((n) => n + 1), relayClockTickMs);
    return () => {
      clearInterval(refresh);
      clearInterval(tick);
    };
  }, [router]);

  if (relays.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-ink-700 p-6 text-center text-sm text-bamboo-200/60">
        {t('empty')}
      </p>
    );
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-ink-800">
      <table className="min-w-full divide-y divide-ink-800 text-sm">
        <thead className="bg-ink-900/40">
          <tr>
            <Th>{t('columns.region')}</Th>
            <Th>{t('columns.hostname')}</Th>
            <Th>{t('columns.port')}</Th>
            <Th>{t('columns.health')}</Th>
            <Th>{t('columns.lastCheck')}</Th>
          </tr>
        </thead>
        <tbody className="divide-y divide-ink-800">
          {relays.map((r) => (
            <RelayRow key={r.id} relay={r} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function RelayRow({ relay }: { relay: Relay }) {
  const t = useTranslations('relays');
  const status: RelayHealthStatus = relay.lastHealthStatus ?? 'unknown';
  return (
    <tr>
      <Td>{relay.region}</Td>
      <Td mono>{relay.hostname}</Td>
      <Td mono>{relay.port}</Td>
      <Td>
        <HealthBadge status={status} />
        {status === 'unhealthy' && relay.lastHealthError && (
          // The reason text lives directly under the badge — same
          // cell, smaller type — so the operator's eye doesn't have
          // to track to a different column to learn the cause.
          // Stage 1 already strips the URL prefix server-side so
          // this stays compact.
          <div className="mt-1 max-w-xs truncate text-xs text-amber-400/80" title={relay.lastHealthError}>
            {relay.lastHealthError}
          </div>
        )}
      </Td>
      <Td>
        {relay.lastHealthCheckAt ? (
          <time
            dateTime={relay.lastHealthCheckAt}
            title={relay.lastHealthCheckAt}
            className="text-xs text-bamboo-200/70"
          >
            {formatRelative(relay.lastHealthCheckAt)}
          </time>
        ) : (
          <span className="text-xs text-bamboo-200/40">{t('lastCheck.never')}</span>
        )}
      </Td>
    </tr>
  );
}

// HealthBadge mirrors the PeerTable status-pill vocabulary
// (rounded-full border with tone-matched outline + text). Three
// tones because the wire model has three states, not the binary
// up/down a simpler design would conflate; "unknown" deserves its
// own color because operators MUST be able to tell "we don't know
// yet" from "we've confirmed it's down" at a glance — those imply
// different on-call actions.
function HealthBadge({ status }: { status: RelayHealthStatus }) {
  const t = useTranslations('relays.health');
  const cls = {
    healthy: 'border-bamboo-300/60 text-bamboo-200',
    unhealthy: 'border-amber-500/60 text-amber-400',
    unknown: 'border-bamboo-200/30 text-bamboo-200/60',
  }[status];
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium ${cls}`}
    >
      {t(status)}
    </span>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return (
    <th className="px-4 py-2 text-left text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
      {children}
    </th>
  );
}

function Td({ children, mono = false }: { children: React.ReactNode; mono?: boolean }) {
  return (
    <td className={`px-4 py-3 text-bamboo-50 ${mono ? 'font-mono text-xs' : ''}`}>
      {children}
    </td>
  );
}

// formatRelative renders an ISO timestamp as a terse "Ns ago" /
// "Nm ago" / "Nh ago" string. Health probes fire every 30s so the
// most common case is the "Ns ago" shape; a relay row stuck at "Nh
// ago" is itself a smell (reaper goroutine wedged).
function formatRelative(iso: string): string {
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return iso;
  const seconds = Math.max(0, (Date.now() - then) / 1000);
  if (seconds < 60) return `${Math.round(seconds)}s ago`;
  const minutes = seconds / 60;
  if (minutes < 60) return `${Math.round(minutes)}m ago`;
  const hours = minutes / 60;
  if (hours < 48) return `${Math.round(hours)}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}
