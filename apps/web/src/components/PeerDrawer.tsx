// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useEffect, useRef } from 'react';
import { useTranslations } from 'next-intl';
import type { Peer } from '@/lib/types';

type Props = {
  peer: Peer | null;
  open: boolean;
  onClose: () => void;
};

// PeerDrawer is the slide-in side panel that appears when a row is
// selected. `open` is driven by the URL (`?selected=<id>`) so back/
// forward and link-sharing both work; `peer` is null when the id
// resolved to 404 (deleted peer or stale link), and the drawer
// renders a not-found state in that case.
export function PeerDrawer({ peer, open, onClose }: Props) {
  const t = useTranslations('peers.drawer');
  const tStatus = useTranslations('peers.status');
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose();
    }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  return (
    <div
      aria-hidden={!open}
      className={`fixed inset-0 z-40 transition ${open ? 'pointer-events-auto' : 'pointer-events-none'}`}
    >
      <div
        className={`absolute inset-0 bg-zinc-900/40 transition-opacity dark:bg-black/60 ${
          open ? 'opacity-100' : 'opacity-0'
        }`}
        onClick={onClose}
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="peer-drawer-title"
        className={`absolute right-0 top-0 flex h-full w-full max-w-md flex-col border-l border-zinc-200 bg-white shadow-xl transition-transform dark:border-zinc-800 dark:bg-zinc-950 ${
          open ? 'translate-x-0' : 'translate-x-full'
        }`}
      >
        <DrawerHeader peer={peer} statusLabel={peer ? tStatus(peer.status) : ''} onClose={onClose} closeLabel={t('close')} />
        <div className="flex-1 overflow-y-auto px-6 py-4">
          {peer ? <DrawerBody peer={peer} /> : <NotFoundState message={t('notFound')} />}
        </div>
      </div>
    </div>
  );
}

function DrawerHeader({
  peer,
  statusLabel,
  onClose,
  closeLabel,
}: {
  peer: Peer | null;
  statusLabel: string;
  onClose: () => void;
  closeLabel: string;
}) {
  return (
    <header className="flex items-start justify-between border-b border-zinc-200 px-6 py-4 dark:border-zinc-800">
      <div className="min-w-0">
        <h2
          id="peer-drawer-title"
          className="truncate text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-100"
        >
          {peer?.hostname ?? '—'}
        </h2>
        {peer && (
          <div className="mt-1 flex items-center gap-2">
            <StatusBadge status={peer.status} label={statusLabel} />
            <span className="font-mono text-xs text-zinc-500 dark:text-zinc-400">{peer.ip}</span>
          </div>
        )}
      </div>
      <button
        type="button"
        onClick={onClose}
        aria-label={closeLabel}
        className="-mr-2 -mt-1 rounded p-2 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
      >
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
          <path d="M3 3l10 10M13 3L3 13" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      </button>
    </header>
  );
}

function DrawerBody({ peer }: { peer: Peer }) {
  const t = useTranslations('peers.drawer');
  return (
    <div className="space-y-6">
      <Section title={t('sections.basic')}>
        <Field label={t('fields.hostname')} value={peer.hostname} />
        <Field label={t('fields.ip')} value={peer.ip} mono />
        <Field label={t('fields.os')} value={peer.os || '—'} />
        <Field label={t('fields.clientVersion')} value={peer.clientVersion || '—'} mono />
        <Field
          label={t('fields.tags')}
          value={
            peer.tags.length === 0 ? (
              <span className="text-zinc-500 dark:text-zinc-400">{t('empty.tags')}</span>
            ) : (
              <div className="flex flex-wrap gap-1">
                {peer.tags.map((tag) => (
                  <span
                    key={tag}
                    className="rounded bg-zinc-100 px-2 py-0.5 text-xs text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300"
                  >
                    {tag}
                  </span>
                ))}
              </div>
            )
          }
        />
      </Section>

      <Section title={t('sections.wireguard')}>
        <Field
          label={t('fields.publicKey')}
          value={
            peer.wireguardPublicKey ? (
              <code className="block break-all rounded bg-zinc-100 px-2 py-1 font-mono text-xs text-zinc-800 dark:bg-zinc-900 dark:text-zinc-200">
                {peer.wireguardPublicKey}
              </code>
            ) : (
              '—'
            )
          }
        />
      </Section>

      <Section title={t('sections.endpoints')}>
        {peer.endpoints.length === 0 ? (
          <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('empty.endpoints')}</p>
        ) : (
          <ul className="space-y-1">
            {peer.endpoints.map((ep) => (
              <li
                key={ep}
                className="rounded bg-zinc-100 px-2 py-1 font-mono text-xs text-zinc-800 dark:bg-zinc-900 dark:text-zinc-200"
              >
                {ep}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t('sections.timestamps')}>
        <Field label={t('fields.createdAt')} value={formatTimestamp(peer.createdAt)} />
        <Field
          label={t('fields.lastSeen')}
          value={peer.lastSeenAt ? formatTimestamp(peer.lastSeenAt) : '—'}
        />
      </Section>

      <Section title={t('sections.actions')}>
        <ActionPlaceholder label={t('actions.rename')} title={t('actions.comingSoon')} />
        <ActionPlaceholder label={t('actions.disable')} title={t('actions.comingSoon')} />
        <ActionPlaceholder label={t('actions.delete')} title={t('actions.comingSoon')} danger />
      </Section>
    </div>
  );
}

function NotFoundState({ message }: { message: string }) {
  return (
    <div className="flex h-full items-center justify-center">
      <p className="text-center text-sm text-zinc-500 dark:text-zinc-400">{message}</p>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-2">
      <h3 className="text-xs font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
        {title}
      </h3>
      <div className="space-y-2">{children}</div>
    </section>
  );
}

function Field({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
      <dt className="text-zinc-500 dark:text-zinc-400">{label}</dt>
      <dd className={`min-w-0 break-words text-zinc-900 dark:text-zinc-100 ${mono ? 'font-mono text-xs' : ''}`}>
        {value}
      </dd>
    </div>
  );
}

function ActionPlaceholder({
  label,
  title,
  danger = false,
}: {
  label: string;
  title: string;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      disabled
      title={title}
      className={`w-full cursor-not-allowed rounded-md border px-3 py-1.5 text-left text-sm opacity-60 ${
        danger
          ? 'border-red-200 text-red-700 dark:border-red-900/50 dark:text-red-400'
          : 'border-zinc-200 text-zinc-700 dark:border-zinc-800 dark:text-zinc-300'
      }`}
    >
      {label}
    </button>
  );
}

function StatusBadge({ status, label }: { status: Peer['status']; label: string }) {
  const tone = {
    online: 'bg-bamboo-100 text-bamboo-800 dark:bg-bamboo-900/40 dark:text-bamboo-300',
    offline: 'bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400',
    disabled: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
  }[status];
  return <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${tone}`}>{label}</span>;
}

function formatTimestamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toISOString().replace('T', ' ').replace(/\..+$/, ' UTC');
}
