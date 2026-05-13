// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useTranslations } from 'next-intl';
import type { Invitation } from '@/lib/types';

type Status = 'pending' | 'accepted' | 'revoked' | 'expired';

// InvitationsTable mirrors PreAuthKeyTable's visual vocabulary
// (hairline header, divide-y rows, dot+label status) so the audit
// surface reads identically. Status is derived per row from the
// timestamps — the controller intentionally doesn't ship a status
// column, same as preauth keys.
//
// No revoke column for v1: the backend revoke endpoint is on the
// follow-up roadmap (see PR #83 description). When it lands, the
// revoke button drops into the rightmost column with the same
// outlined-on-hover-red pattern as RevokeButton in PreAuthKeyTable.
export function InvitationsTable({ invitations }: { invitations: Invitation[] }) {
  const t = useTranslations('users.invitations');
  if (invitations.length === 0) {
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
            <th className="px-4 py-3 font-medium">{t('columns.email')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.role')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.status')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.invitedBy')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.createdAt')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.expiresAt')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-200 dark:divide-zinc-800">
          {invitations.map((inv) => (
            <InvitationRow key={inv.id} inv={inv} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function InvitationRow({ inv }: { inv: Invitation }) {
  const tRole = useTranslations('users.role');
  const status = statusOf(inv);
  return (
    <tr className="text-zinc-700 dark:text-zinc-300">
      <td className="px-4 py-3 align-top text-zinc-900 dark:text-zinc-100">{inv.email}</td>
      <td className="px-4 py-3 align-top text-xs text-zinc-500 dark:text-zinc-400">
        {tRole(inv.isAdmin ? 'admin' : 'member')}
      </td>
      <td className="px-4 py-3 align-top">
        <StatusBadge status={status} />
      </td>
      <td className="px-4 py-3 align-top text-xs text-zinc-500 dark:text-zinc-400">
        {inv.invitedBy ? (
          <span className="font-mono">{inv.invitedBy.slice(0, 8)}</span>
        ) : (
          '—'
        )}
      </td>
      <td className="px-4 py-3 align-top text-xs text-zinc-500 dark:text-zinc-400">
        {formatTimestamp(inv.createdAt)}
      </td>
      <td className="px-4 py-3 align-top text-xs text-zinc-500 dark:text-zinc-400">
        {formatTimestamp(inv.expiresAt)}
      </td>
    </tr>
  );
}

function StatusBadge({ status }: { status: Status }) {
  const t = useTranslations('users.invitations.status');
  // Dot vocabulary mirrors PreAuthKeyTable:
  //   pending  → bamboo-500 ring  (alive, pre-active — token not yet redeemed)
  //   accepted → zinc-500 solid    (terminal-good)
  //   revoked  → hollow zinc ring  (struck off)
  //   expired  → zinc-300 solid    (faded / lapsed)
  const dot: Record<Status, string> = {
    pending: 'border border-bamboo-500 bg-transparent',
    accepted: 'bg-zinc-500 dark:bg-zinc-400',
    revoked: 'border border-zinc-400 bg-transparent dark:border-zinc-500',
    expired: 'bg-zinc-300 dark:bg-zinc-600',
  };
  return (
    <span className="inline-flex items-center gap-2 text-xs text-zinc-700 dark:text-zinc-300">
      <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot[status]}`} />
      {t(status)}
    </span>
  );
}

function statusOf(inv: Invitation): Status {
  if (inv.revokedAt) return 'revoked';
  if (inv.acceptedAt) return 'accepted';
  if (inv.expiresAt && new Date(inv.expiresAt).getTime() < Date.now()) return 'expired';
  return 'pending';
}

function formatTimestamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toISOString().replace('T', ' ').replace(/\..+$/, ' UTC');
}
