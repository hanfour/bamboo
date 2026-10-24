// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { revokeInvitationAction } from '@/lib/actions';
import type { Invitation } from '@/lib/types';

type Status = 'pending' | 'accepted' | 'revoked' | 'expired';

// InvitationsTable mirrors PreAuthKeyTable's visual vocabulary
// (hairline header, divide-y rows, dot+label status) so the audit
// surface reads identically. Status is derived per row from the
// timestamps — the controller intentionally doesn't ship a status
// column, same as preauth keys.
//
// Revoke action lives in the rightmost column, only rendered for
// pending invitations. Accepted / revoked / expired rows show"—"
// to keep the column aligned without an active control.
export function InvitationsTable({ invitations }: { invitations: Invitation[] }) {
 const t = useTranslations('users.invitations');
 if (invitations.length === 0) {
 return (
 <p className="rounded-2xl border border-dashed border-[#777]/30 p-10 text-center text-[13px] text-bamboo-200/60">
 {t('empty')}
 </p>
 );
 }
 return (
 <div className="overflow-x-auto">
 <table className="w-full text-[13px]">
 <thead className="border-b border-[#777]/20 text-left text-xs font-medium text-bamboo-200/50">
 <tr>
 <th className="px-4 py-3 font-medium">{t('columns.email')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.role')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.status')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.invitedBy')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.createdAt')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.expiresAt')}</th>
 <th className="px-4 py-3 font-medium sr-only">{t('columns.actions')}</th>
 </tr>
 </thead>
 <tbody className="divide-y divide-[#777]/20">
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
 const tActions = useTranslations('users.invitations.actions');
 const status = statusOf(inv);
 return (
 <tr className="text-bamboo-100">
 <td className="px-4 py-3 align-top text-bamboo-50">{inv.email}</td>
 <td className="px-4 py-3 align-top text-xs text-bamboo-200/60">
 {tRole(inv.isAdmin ? 'admin' : 'member')}
 </td>
 <td className="px-4 py-3 align-top">
 <StatusBadge status={status} />
 </td>
 <td className="px-4 py-3 align-top text-xs text-bamboo-200/60">
 {inv.invitedBy ? (
 <span className="font-mono">{inv.invitedBy.slice(0, 8)}</span>
 ) : (
 '—'
 )}
 </td>
 <td className="px-4 py-3 align-top text-xs text-bamboo-200/60">
 {formatTimestamp(inv.createdAt)}
 </td>
 <td className="px-4 py-3 align-top text-xs text-bamboo-200/60">
 {formatTimestamp(inv.expiresAt)}
 </td>
 <td className="px-4 py-3 align-top text-right">
 {status === 'pending' ? (
 <RevokeButton id={inv.id} />
 ) : (
 <span className="text-xs text-bamboo-200/40">{tActions('noAction')}</span>
 )}
 </td>
 </tr>
 );
}

function RevokeButton({ id }: { id: string }) {
 // Two-stage confirm pattern mirrors PreAuthKeyTable.RevokeButton —
 // first click reveals 確定撤銷 / 取消, second click on confirm fires
 // the action. Avoids a full modal for one destructive verb. Default
 // is outlined zinc with red text-on-hover; confirm is outlined
 // red-300. Never a solid red fill — Muji discipline.
 const t = useTranslations('users.invitations.actions');
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
 const res = await revokeInvitationAction(id);
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
 {pending ? t('working') : t('confirmRevoke')}
 </button>
 <button
 type="button"
 disabled={pending}
 onClick={() => {
 setConfirming(false);
 setError(null);
 }}
 className="rounded-full bg-[#777]/20 px-2 py-1 text-xs text-bamboo-100 transition-colors hover:bg-[#777]/30 hover:text-bamboo-50 disabled:opacity-50"
 >
 {t('cancel')}
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
 className="rounded-full bg-[#777]/20 px-2 py-1 text-xs text-bamboo-100 transition-colors hover:bg-red-500/15 hover:text-red-400"
 >
 {t('revoke')}
 </button>
 );
}

function StatusBadge({ status }: { status: Status }) {
 const t = useTranslations('users.invitations.status');
 // Dot vocabulary mirrors PreAuthKeyTable:
 // pending → bamboo-500 ring (alive, pre-active — token not yet redeemed)
 // accepted → zinc-500 solid (terminal-good)
 // revoked → hollow zinc ring (struck off)
 // expired → zinc-300 solid (faded / lapsed)
 const dot: Record<Status, string> = {
 pending: 'border border-bamboo-500 bg-transparent',
 accepted: 'bg-ink-9000 ',
 revoked: 'border border-zinc-400 bg-transparent ',
 expired: 'bg-zinc-300 ',
 };
 return (
 <span className="inline-flex items-center gap-2 text-xs text-bamboo-100">
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
