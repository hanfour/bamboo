// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useTranslations } from 'next-intl';
import type { Peer } from '@/lib/types';

type Props = {
  peers: Peer[];
  selectedId?: string;
  onSelect: (id: string) => void;
};

export function PeerTable({ peers, selectedId, onSelect }: Props) {
  const t = useTranslations('peers');

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
        <thead className="bg-zinc-50 text-left text-xs font-medium uppercase tracking-wide text-zinc-500 dark:bg-zinc-900 dark:text-zinc-400">
          <tr>
            <th className="px-4 py-3">{t('columns.hostname')}</th>
            <th className="px-4 py-3 font-mono normal-case">{t('columns.ip')}</th>
            <th className="px-4 py-3">{t('columns.tags')}</th>
            <th className="px-4 py-3">{t('columns.os')}</th>
            <th className="px-4 py-3">{t('columns.status')}</th>
            <th className="px-4 py-3">{t('columns.lastSeen')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-200 dark:divide-zinc-800">
          {peers.map((p) => {
            const isSelected = p.id === selectedId;
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
                className={`cursor-pointer text-zinc-700 transition-colors focus:outline-none focus:ring-1 focus:ring-bamboo-500 dark:text-zinc-300 ${
                  isSelected
                    ? 'bg-bamboo-50 hover:bg-bamboo-100 dark:bg-bamboo-900/20 dark:hover:bg-bamboo-900/30'
                    : 'hover:bg-zinc-50 dark:hover:bg-zinc-900'
                }`}
              >
                <td className="px-4 py-3 font-medium text-zinc-900 dark:text-zinc-100">
                  {p.hostname}
                </td>
                <td className="px-4 py-3 font-mono text-xs">{p.ip}</td>
                <td className="px-4 py-3">
                  <div className="flex flex-wrap gap-1">
                    {p.tags.map((tag) => (
                      <span
                        key={tag}
                        className="rounded bg-zinc-100 px-2 py-0.5 text-xs text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300"
                      >
                        {tag}
                      </span>
                    ))}
                  </div>
                </td>
                <td className="px-4 py-3 text-xs text-zinc-500 dark:text-zinc-400">{p.os}</td>
                <td className="px-4 py-3">
                  <StatusBadge status={p.status} label={t(`status.${p.status}`)} />
                </td>
                <td className="px-4 py-3 text-xs text-zinc-500 dark:text-zinc-400">
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

function StatusBadge({ status, label }: { status: Peer['status']; label: string }) {
  const tone = {
    online: 'bg-bamboo-100 text-bamboo-800 dark:bg-bamboo-900/40 dark:text-bamboo-300',
    offline: 'bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400',
    disabled: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
  }[status];
  return (
    <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${tone}`}>
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
