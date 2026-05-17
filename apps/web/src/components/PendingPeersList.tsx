// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import type { Peer } from '@/lib/types';
import { approvePeerAction, rejectPeerAction } from '@/lib/actions';

// PendingPeersList renders the device-approval queue (issue #133) at
// the top of /peers. Visual contract:
//
// - Section sits above the main PeerTable, framed by an `ink-700`
//   hairline so admins read it as a distinct surface — but no
//   filled accent. The bamboo cream stripe on the left (same accent
//   the Sidebar active item uses) is the sole color cue.
// - Each row carries the hostname + dns-name + os + owner + when-
//   requested. Right side: Approve (primary) + Reject (ghost).
//   Per-row spinner during the in-flight transition; row-level
//   error message inline if the server action returns ok:false.
// - No table — rows are flex containers. The pending queue is
//   inherently low-density (admins shouldn't see >10 pending peers
//   in normal operation; if they do, that's a sign of a CI key
//   that should be auto-approve).
export function PendingPeersList({ peers }: { peers: Peer[] }) {
  const t = useTranslations('peers.pending');

  return (
    <section className="space-y-3">
      <header className="flex items-baseline justify-between">
        <h2 className="text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
          {t('title', { count: peers.length })}
        </h2>
        <span className="text-xs text-bamboo-200/50">{t('subtitle')}</span>
      </header>
      <div className="overflow-hidden rounded-md border border-ink-700/70">
        {peers.map((peer, idx) => (
          <PendingPeerRow
            key={peer.id}
            peer={peer}
            isLast={idx === peers.length - 1}
          />
        ))}
      </div>
    </section>
  );
}

function PendingPeerRow({ peer, isLast }: { peer: Peer; isLast: boolean }) {
  const t = useTranslations('peers.pending');
  const [error, setError] = useState<string | null>(null);
  // useTransition wraps the server action so the row can show a
  // pending state without blocking the rest of the UI; we don't
  // distinguish approve-pending from reject-pending in the spinner
  // because clicking either while the other is in-flight is a
  // user-error case (debounce to one click at a time).
  const [isPending, startTransition] = useTransition();

  const submit = (verb: 'approve' | 'reject') => {
    setError(null);
    startTransition(async () => {
      const action = verb === 'approve' ? approvePeerAction : rejectPeerAction;
      const r = await action(peer.id);
      if (!r.ok) {
        setError(r.error);
      }
    });
  };

  // 2px bamboo-400 left stripe — same affordance the sidebar active
  // item uses, signaling "this row needs attention".
  return (
    <div
      className={[
        'relative flex flex-wrap items-center gap-4 bg-ink-900/50 px-5 py-4',
        'before:absolute before:inset-y-2 before:left-0 before:w-0.5 before:rounded-full before:bg-bamboo-400',
        !isLast && 'border-b border-ink-800',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex items-baseline gap-3">
          <span className="font-medium text-bamboo-50">{peer.hostname}</span>
          {peer.peerDnsName && (
            <span className="font-mono text-xs">
              <span className="text-bamboo-200/80">{peer.peerDnsName}</span>
              <span className="text-bamboo-200/40">.bamboo</span>
            </span>
          )}
        </div>
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-bamboo-200/60">
          <span>{peer.os}</span>
          <span className="font-mono">{peer.ip}</span>
          {peer.ownerEmail && (
            <span title={peer.ownerEmail}>
              {t('requestedBy', {
                actor: peer.ownerDisplayName || peer.ownerEmail,
              })}
            </span>
          )}
          {/* suppressHydrationWarning: the SSR pass and the client
              hydration tick are seconds apart, so "8s ago" on the
              server can hydrate as "9s ago" on the client. The diff
              is cosmetic; suppressing avoids the noisy console while
              keeping the same render path. */}
          <span suppressHydrationWarning>
            {t('registered', { when: formatRelative(peer.createdAt) })}
          </span>
        </div>
        {error && (
          <p className="text-xs text-red-300/80" role="alert">
            {error}
          </p>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <button
          type="button"
          onClick={() => submit('reject')}
          disabled={isPending}
          className="rounded border border-ink-700 px-3 py-1.5 text-xs text-bamboo-200/80 transition-colors hover:bg-ink-800 hover:text-bamboo-50 focus:outline-none focus-visible:ring-1 focus-visible:ring-bamboo-400 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {t('reject')}
        </button>
        <button
          type="button"
          onClick={() => submit('approve')}
          disabled={isPending}
          className="rounded border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-xs font-medium text-ink-950 transition-colors hover:bg-bamboo-100 focus:outline-none focus-visible:ring-1 focus-visible:ring-bamboo-400 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {isPending ? t('working') : t('approve')}
        </button>
      </div>
    </div>
  );
}

function formatRelative(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86_400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86_400)}d ago`;
}
