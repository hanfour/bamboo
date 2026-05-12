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
            <Th>{t('columns.description')}</Th>
            <Th>{t('columns.status')}</Th>
            <Th>{t('columns.kind')}</Th>
            <Th>{t('columns.useCount')}</Th>
            <Th>{t('columns.createdAt')}</Th>
            <Th>
              <span className="sr-only">{t('columns.actions')}</span>
            </Th>
          </tr>
        </thead>
        <tbody className="divide-y divide-white/[0.04]">
          {keys.map((k) => (
            <PreAuthKeyRow key={k.id} k={k} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return (
    <th className="px-5 py-3.5 font-mono text-[10px] font-medium uppercase tracking-[0.22em] text-zinc-500">
      {children}
    </th>
  );
}

function PreAuthKeyRow({ k }: { k: PreAuthKey }) {
  const t = useTranslations('preAuthKeys');
  return (
    <tr className="text-zinc-300 hover:bg-white/[0.02]">
      <td className="px-5 py-4 text-zinc-100">
        {k.description || <span className="text-zinc-600">—</span>}
      </td>
      <td className="px-5 py-4">
        <StatusBadge status={statusOf(k)} t={t} />
      </td>
      <td className="px-5 py-4 font-mono text-[11px] uppercase tracking-[0.16em] text-zinc-500">
        {k.reusable ? t('kind.reusable') : t('kind.oneShot')}
      </td>
      <td className="px-5 py-4 font-mono text-[12px] text-zinc-300">{k.useCount}</td>
      <td className="px-5 py-4 font-mono text-[11px] text-zinc-500">
        {formatTimestamp(k.createdAt)}
      </td>
      <td className="px-5 py-4 text-right">
        {statusOf(k) === 'revoked' ? (
          <span className="font-mono text-[11px] uppercase tracking-[0.18em] text-zinc-600">
            {t('actions.alreadyRevoked')}
          </span>
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
          className="rounded-md border border-red-400/40 bg-red-500/[0.10] px-2.5 py-1 font-mono text-[11px] uppercase tracking-[0.18em] text-red-300 transition hover:bg-red-500/[0.16] disabled:opacity-50"
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
          className="rounded-md border border-white/[0.08] px-2.5 py-1 font-mono text-[11px] uppercase tracking-[0.18em] text-zinc-400 transition hover:bg-white/[0.04] disabled:opacity-50"
        >
          {t('actions.cancel')}
        </button>
        {error && (
          <span className="text-xs text-red-300" title={error}>
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
      className="rounded-md border border-white/[0.08] px-2.5 py-1 font-mono text-[11px] uppercase tracking-[0.18em] text-zinc-400 transition hover:border-red-400/30 hover:bg-red-500/[0.06] hover:text-red-300"
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
  const tone: Record<Status, { wrap: string; dot: string }> = {
    revoked: {
      wrap: 'border-white/[0.08] bg-white/[0.02] text-zinc-500',
      dot: 'bg-zinc-600',
    },
    expired: {
      wrap: 'border-white/[0.08] bg-white/[0.02] text-zinc-500',
      dot: 'bg-zinc-600',
    },
    used: {
      wrap: 'border-amber-400/40 bg-amber-500/[0.08] text-amber-300',
      dot: 'bg-amber-400',
    },
    reusable: {
      wrap: 'border-bamboo-400/40 bg-bamboo-500/[0.08] text-bamboo-300',
      dot: 'bg-bamboo-400',
    },
    pending: {
      wrap: 'border-bamboo-400/40 bg-bamboo-500/[0.08] text-bamboo-300',
      dot: 'bg-bamboo-400',
    },
  };
  const { wrap, dot } = tone[status];
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.16em] ${wrap}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${dot}`} aria-hidden />
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
