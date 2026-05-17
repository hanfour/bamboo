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
// - revokedAt set → revoked
// - expiresAt set + past → expired
// - !reusable && useCount > 0 → used (one-shot consumed)
// - reusable && useCount > 0 → reusable (active, has redeems)
// - else → pending (never used yet)
// Keeping the derivation in the renderer means the controller
// doesn't have to commit to a status enum on the wire.
export function PreAuthKeyTable({ keys }: { keys: PreAuthKey[] }) {
 const t = useTranslations('preAuthKeys');
 if (keys.length === 0) {
 return (
 <p className="rounded-lg border border-dashed border-bamboo-200/30 p-8 text-center text-sm text-bamboo-200/60 dark:text-bamboo-200/40">
 {t('empty')}
 </p>
 );
 }
 return (
 <div className="overflow-x-auto rounded-lg border border-ink-800">
 <table className="w-full text-sm">
 <thead className="border-b border-ink-800 text-left text-xs font-medium uppercase tracking-wide text-bamboo-200/60 dark:text-bamboo-200/40">
 <tr>
 <th className="px-4 py-3 font-medium">{t('columns.description')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.status')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.kind')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.useCount')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.createdAt')}</th>
 <th className="px-4 py-3 sr-only">{t('columns.actions')}</th>
 </tr>
 </thead>
 <tbody className="divide-y divide-ink-800/70">
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
 <tr className="text-bamboo-100">
 <td className="px-4 py-3 text-bamboo-50">
 {k.description || <span className="text-bamboo-200/40">—</span>}
 </td>
 <td className="px-4 py-3">
 <StatusBadge status={statusOf(k)} t={t} />
 </td>
 <td className="px-4 py-3 text-xs text-bamboo-200/60">
 {k.reusable ? t('kind.reusable') : t('kind.oneShot')}
 </td>
 <td className="px-4 py-3 text-xs font-mono">{k.useCount}</td>
 <td className="px-4 py-3 text-xs text-bamboo-200/60">
 {formatTimestamp(k.createdAt)}
 </td>
 <td className="px-4 py-3 text-right">
 {statusOf(k) === 'revoked' ? (
 <span className="text-xs text-bamboo-200/40">{t('actions.alreadyRevoked')}</span>
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

 // Muji destructive-action treatment: never solid red. Revoke trigger
 // is a plain outlined button whose text colors to red-600 only on
 // hover. Confirm is an outlined red border + red-700 text — solid
 // enough to signal"this is the destructive step" without breaking
 // the Muji flat palette.
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
 className="rounded-md border border-red-300 px-2 py-1 text-xs font-medium text-red-700 transition-colors hover:border-red-400 hover:bg-red-50 disabled:opacity-50 dark:border-red-900/50 dark:text-red-400 dark:hover:bg-red-950/40"
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
 className="rounded-md border border-bamboo-200/30 px-2 py-1 text-xs text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 disabled:opacity-50 dark:text-bamboo-100 dark:hover:text-bamboo-50"
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
 className="rounded-md border border-bamboo-200/30 px-2 py-1 text-xs text-bamboo-100 transition-colors hover:border-red-300 hover:text-red-700 dark:text-bamboo-100 dark:hover:border-red-900/50 dark:hover:text-red-400"
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
 // Same dot + label pattern as PeerTable. bamboo-500 dot reserved for
 // the two"alive" states (reusable, pending). Terminal states use
 // various zinc tones; revoked gets a hollow dot to communicate
 //"struck off / closed".
 const dot: Record<Status, string> = {
 revoked: 'border border-zinc-400 bg-transparent ',
 expired: 'bg-zinc-300 ',
 used: 'bg-ink-9000 ',
 reusable: 'bg-bamboo-500',
 pending: 'border border-bamboo-500 bg-transparent',
 };
 return (
 <span className="inline-flex items-center gap-2 text-xs text-bamboo-100">
 <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot[status]}`} />
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
