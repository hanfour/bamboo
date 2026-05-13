// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import type { Peer } from '@/lib/types';

type Props = {
  peers: Peer[];
  selectedId?: string;
  onSelect: (id: string) => void;
};

export function PeerTable({ peers, selectedId, onSelect }: Props) {
  const t = useTranslations('peers');
  // expandedId tracks which row's address-details disclosure is open.
  // Only one row can be open at a time — clicking another row's
  // chevron auto-closes the previous one. The row-click handler
  // (drawer open) is unaffected; chevron click stops propagation so
  // the disclosure can toggle without opening the drawer.
  const [expandedId, setExpandedId] = useState<string | null>(null);

  if (peers.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-zinc-300 p-8 text-center text-sm text-zinc-500 dark:border-zinc-700 dark:text-zinc-400">
        {t('empty')}
      </p>
    );
  }

  return (
    <div className="overflow-x-auto rounded-lg border border-zinc-200 dark:border-zinc-800">
      <table className="w-full text-sm">
        <thead className="border-b border-zinc-200 text-left text-xs font-medium uppercase tracking-wide text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
          <tr>
            <th className="px-4 py-3 font-medium">{t('columns.hostname')}</th>
            <th className="px-4 py-3 font-mono font-medium normal-case">{t('columns.ip')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.owner')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.tags')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.os')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.status')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.lastSeen')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-200 dark:divide-zinc-800">
          {peers.map((p) => {
            const isSelected = p.id === selectedId;
            const isExpanded = expandedId === p.id;
            // Selected-row treatment: a 2px bamboo-500 stripe on the
            // left edge (single accent) plus a zinc-50 fill — no
            // bamboo-tinted background so the row stays Muji-neutral.
            return (
              <tr
                key={p.id}
                onClick={() => onSelect(p.id)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onSelect(p.id);
                  }
                }}
                tabIndex={0}
                role="button"
                aria-pressed={isSelected}
                className={`relative cursor-pointer text-zinc-700 transition-colors focus:outline-none focus:ring-1 focus:ring-bamboo-500 dark:text-zinc-300 ${
                  isSelected
                    ? 'bg-zinc-50 hover:bg-zinc-100 before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-bamboo-500 dark:bg-zinc-900 dark:hover:bg-zinc-800'
                    : 'hover:bg-zinc-50 dark:hover:bg-zinc-900'
                }`}
              >
                <td className="px-4 py-3 align-top font-medium text-zinc-900 dark:text-zinc-100">
                  {p.hostname}
                </td>
                <td className="px-4 py-3 align-top">
                  <AddressCell
                    peer={p}
                    expanded={isExpanded}
                    onToggle={(e) => {
                      e.stopPropagation();
                      setExpandedId((curr) => (curr === p.id ? null : p.id));
                    }}
                  />
                </td>
                <td className="px-4 py-3 align-top text-xs text-zinc-500 dark:text-zinc-400">
                  {p.ownerEmail ? (
                    <span title={p.ownerEmail} className="truncate text-zinc-700 dark:text-zinc-300">
                      {p.ownerDisplayName || p.ownerEmail}
                    </span>
                  ) : (
                    <span className="text-zinc-400" title={t('ownerUnknown')}>
                      {t('ownerUnknown')}
                    </span>
                  )}
                </td>
                <td className="px-4 py-3 align-top">
                  <div className="flex flex-wrap gap-1">
                    {p.tags.map((tag) => (
                      <span
                        key={tag}
                        className="rounded border border-zinc-200 px-2 py-0.5 text-xs text-zinc-600 dark:border-zinc-700 dark:text-zinc-400"
                      >
                        {tag}
                      </span>
                    ))}
                  </div>
                </td>
                <td className="px-4 py-3 align-top text-xs text-zinc-500 dark:text-zinc-400">{p.os}</td>
                <td className="px-4 py-3 align-top">
                  <StatusBadge status={p.status} label={t(`status.${p.status}`)} />
                </td>
                <td className="px-4 py-3 align-top text-xs text-zinc-500 dark:text-zinc-400">
                  {p.lastSeenAt ? formatRelative(p.lastSeenAt) : '—'}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// AddressCell shows the peer's primary IP with a chevron disclosure
// that expands to reveal the WireGuard public key and the short id.
// Matches Tailscale's "addresses" column where one click on the
// chevron pops out the DNS short name + machine key; we expose the
// equivalent fields (pubkey + short id) until we ship MagicDNS-style
// short names.
//
// The chevron is a real <button>, so Tab/Enter/Space work for
// keyboard activation. stopPropagation on the click prevents the
// surrounding <tr> from opening the drawer.
function AddressCell({
  peer,
  expanded,
  onToggle,
}: {
  peer: Peer;
  expanded: boolean;
  onToggle: (e: React.MouseEvent | React.KeyboardEvent) => void;
}) {
  const hasDetails = Boolean(peer.wireguardPublicKey);
  return (
    <div className="flex items-start gap-1">
      <div className="min-w-0 flex-1">
        <div className="font-mono text-xs text-zinc-700 dark:text-zinc-300">{peer.ip}</div>
        {expanded && hasDetails && (
          <dl className="mt-1.5 space-y-1 text-[10px] text-zinc-500 dark:text-zinc-400">
            <div>
              <dt className="uppercase tracking-wide text-zinc-400 dark:text-zinc-500">ID</dt>
              <dd className="font-mono text-zinc-700 dark:text-zinc-300">
                {peer.id.slice(0, 8)}
              </dd>
            </div>
            <div>
              <dt className="uppercase tracking-wide text-zinc-400 dark:text-zinc-500">Public key</dt>
              <dd className="break-all font-mono text-zinc-700 dark:text-zinc-300">
                {peer.wireguardPublicKey}
              </dd>
            </div>
          </dl>
        )}
      </div>
      {hasDetails && (
        <button
          type="button"
          onClick={onToggle}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.stopPropagation();
            }
          }}
          aria-label="Toggle address details"
          aria-expanded={expanded}
          className="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-zinc-400 transition-colors hover:bg-zinc-100 hover:text-zinc-700 focus:outline-none focus:ring-1 focus:ring-bamboo-500 dark:hover:bg-zinc-800 dark:hover:text-zinc-300"
        >
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
            className={`h-3.5 w-3.5 transition-transform ${expanded ? 'rotate-180' : ''}`}
          >
            <path d="m6 9 6 6 6-6" />
          </svg>
        </button>
      )}
    </div>
  );
}

function StatusBadge({ status, label }: { status: Peer['status']; label: string }) {
  // Dot + label pattern (Cloudflare / Linear style). The dot is the
  // only color signal: bamboo-500 for online (the brand accent reads
  // as "alive"), solid zinc-400 for offline, hollow zinc dot for
  // disabled. No filled pill backgrounds, no amber tint.
  const dot = {
    online: 'bg-bamboo-500',
    offline: 'bg-zinc-400 dark:bg-zinc-500',
    disabled: 'border border-zinc-400 bg-transparent dark:border-zinc-500',
  }[status];
  return (
    <span className="inline-flex items-center gap-2 text-xs text-zinc-700 dark:text-zinc-300">
      <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot}`} />
      {label}
    </span>
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
