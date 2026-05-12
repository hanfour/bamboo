// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { revokePreAuthKeyAction } from '@/lib/actions';
import type { PreAuthKey } from '@/lib/types';

type Status = 'revoked' | 'expired' | 'used' | 'reusable' | 'pending';

// PreAuthKeyTable renders the tenant's pre-auth keys. Status is
// derived client-side from the raw revokedAt / expiresAt /
// useCount / reusable fields:
//   - revokedAt set                → revoked
//   - expiresAt set + past         → expired
//   - !reusable && useCount > 0    → used (one-shot consumed)
//   - reusable && useCount > 0     → reusable (active, has redeems)
//   - else                         → pending (never used yet)
// Keeping the derivation in the renderer means the controller
// doesn't have to commit to a status enum on the wire.
export function PreAuthKeyTable({ keys }: { keys: PreAuthKey[] }) {
  const t = useTranslations('preAuthKeys');
  if (keys.length === 0) {
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
            <th className="px-4 py-3">{t('columns.description')}</th>
            <th className="px-4 py-3">{t('columns.status')}</th>
            <th className="px-4 py-3">{t('columns.kind')}</th>
            <th className="px-4 py-3">{t('columns.useCount')}</th>
            <th className="px-4 py-3">{t('columns.createdAt')}</th>
            <th className="px-4 py-3 sr-only">{t('columns.actions')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-200 dark:divide-zinc-800">
          {keys.map((k) => (
            <PreAuthKeyRow key={k.id} k={k} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function PreAuthKeyRow({ k }: { k: PreAuthKey }) {
  const t = useTranslations('preAuthKeys');
  return (
    <tr className="text-zinc-700 dark:text-zinc-300">
      <td className="px-4 py-3 text-zinc-900 dark:text-zinc-100">
        {k.description || <span className="text-zinc-400">—</span>}
      </td>
      <td className="px-4 py-3">
        <StatusBadge status={statusOf(k)} t={t} />
      </td>
      <td className="px-4 py-3 text-xs text-zinc-500 dark:text-zinc-400">
        {k.reusable ? t('kind.reusable') : t('kind.oneShot')}
      </td>
      <td className="px-4 py-3 text-xs font-mono">{k.useCount}</td>
      <td className="px-4 py-3 text-xs text-zinc-500 dark:text-zinc-400">
        {formatTimestamp(k.createdAt)}
      </td>
      <td className="px-4 py-3 text-right">
        {statusOf(k) === 'revoked' ? (
          <span className="text-xs text-zinc-400">{t('actions.alreadyRevoked')}</span>
        ) : (
          <RevokeButton id={k.id} />
        )}
      </td>
    </tr>
  );
}

function RevokeButton({ id }: { id: string }) {
  const t = useTranslations('preAuthKeys');
  const [confirming, setConfirming] = useState(false);
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<string | null>(null);

  if (confirming) {
    return (
      <span className="inline-flex items-center gap-2">
        <button
          type="button"
          disabled={pending}
          onClick={() => {
            startTransition(async () => {
              const res = await revokePreAuthKeyAction(id);
              if (res.ok) {
                setError(null);
                setConfirming(false);
              } else {
                setError(res.error);
              }
            });
          }}
          className="rounded-md border border-red-600 bg-red-600 px-2 py-1 text-xs font-medium text-white hover:bg-red-700 disabled:opacity-50"
        >
          {pending ? t('actions.working') : t('actions.confirmRevoke')}
        </button>
        <button
          type="button"
          disabled={pending}
          onClick={() => {
            setConfirming(false);
            setError(null);
          }}
          className="rounded-md border border-zinc-200 px-2 py-1 text-xs text-zinc-700 hover:bg-zinc-50 disabled:opacity-50 dark:border-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-900"
        >
          {t('actions.cancel')}
        </button>
        {error && (
          <span className="text-xs text-red-600 dark:text-red-400" title={error}>
            ⚠
          </span>
        )}
      </span>
    );
  }
  return (
    <button
      type="button"
      onClick={() => setConfirming(true)}
      className="rounded-md border border-red-200 px-2 py-1 text-xs text-red-700 hover:bg-red-50 dark:border-red-900/50 dark:text-red-400 dark:hover:bg-red-950/40"
    >
      {t('actions.revoke')}
    </button>
  );
}

function StatusBadge({
  status,
  t,
}: {
  status: Status;
  t: ReturnType<typeof useTranslations>;
}) {
  const tone: Record<Status, string> = {
    revoked: 'bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400',
    expired: 'bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400',
    used: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
    reusable: 'bg-bamboo-100 text-bamboo-800 dark:bg-bamboo-900/40 dark:text-bamboo-300',
    pending: 'bg-bamboo-100 text-bamboo-800 dark:bg-bamboo-900/40 dark:text-bamboo-300',
  };
  return (
    <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${tone[status]}`}>
      {t(`status.${status}`)}
    </span>
  );
}

function statusOf(k: PreAuthKey): Status {
  if (k.revokedAt) return 'revoked';
  if (k.expiresAt && new Date(k.expiresAt).getTime() < Date.now()) return 'expired';
  if (k.useCount > 0) return k.reusable ? 'reusable' : 'used';
  return 'pending';
}

function formatTimestamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toISOString().replace('T', ' ').replace(/\..+$/, ' UTC');
}
