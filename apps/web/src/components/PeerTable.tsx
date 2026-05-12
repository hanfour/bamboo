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
      <p className="rounded-md border border-dashed border-white/[0.08] px-6 py-12 text-center text-sm text-zinc-500">
        {t('empty')}
      </p>
    );
  }

  return (
    <div className="overflow-x-auto rounded-md border border-white/[0.06] bg-white/[0.01]">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-white/[0.06] text-left">
            <Th>{t('columns.hostname')}</Th>
            <Th mono>{t('columns.ip')}</Th>
            <Th>{t('columns.tags')}</Th>
            <Th>{t('columns.os')}</Th>
            <Th>{t('columns.status')}</Th>
            <Th>{t('columns.lastSeen')}</Th>
          </tr>
        </thead>
        <tbody className="divide-y divide-white/[0.04]">
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
                className={`cursor-pointer text-zinc-300 transition-colors focus:outline-none focus:ring-1 focus:ring-bamboo-400/40 ${
                  isSelected
                    ? 'bg-bamboo-500/[0.06] hover:bg-bamboo-500/[0.08]'
                    : 'hover:bg-white/[0.03]'
                }`}
              >
                <td className="px-5 py-4 text-zinc-100">
                  <div className="flex items-center gap-2">
                    {isSelected ? (
                      <span aria-hidden className="h-3 w-px bg-bamboo-400" />
                    ) : null}
                    <span className="font-medium">{p.hostname}</span>
                  </div>
                </td>
                <td className="px-5 py-4 font-mono text-[12px] text-zinc-300">{p.ip}</td>
                <td className="px-5 py-4">
                  <div className="flex flex-wrap gap-1">
                    {p.tags.length === 0 ? (
                      <span className="font-mono text-[10px] text-zinc-600">—</span>
                    ) : (
                      p.tags.map((tag) => (
                        <span
                          key={tag}
                          className="rounded border border-white/[0.06] px-1.5 py-0.5 font-mono text-[10px] text-zinc-300"
                        >
                          {tag}
                        </span>
                      ))
                    )}
                  </div>
                </td>
                <td className="px-5 py-4 text-[12px] text-zinc-500">{p.os}</td>
                <td className="px-5 py-4">
                  <StatusBadge status={p.status} label={t(`status.${p.status}`)} />
                </td>
                <td className="px-5 py-4 font-mono text-[11px] text-zinc-500">
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

function Th({ children, mono = false }: { children: React.ReactNode; mono?: boolean }) {
  return (
    <th
      className={`px-5 py-3.5 font-mono text-[10px] font-medium uppercase tracking-[0.22em] text-zinc-500 ${
        mono ? 'normal-case tracking-[0.18em]' : ''
      }`}
    >
      {children}
    </th>
  );
}

function StatusBadge({ status, label }: { status: Peer['status']; label: string }) {
  const tone = {
    online: 'border-bamboo-400/40 bg-bamboo-500/[0.08] text-bamboo-300',
    offline: 'border-white/[0.08] bg-white/[0.02] text-zinc-500',
    disabled: 'border-amber-400/40 bg-amber-500/[0.08] text-amber-300',
  }[status];
  const dot = {
    online: 'bg-bamboo-400',
    offline: 'bg-zinc-600',
    disabled: 'bg-amber-400',
  }[status];
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.16em] ${tone}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${dot}`} aria-hidden />
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
