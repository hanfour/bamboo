// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { eraseUserAction } from '@/lib/actions';
import type { User } from '@/lib/types';

// UsersTable mirrors PeerTable's visual vocabulary (hairline border,
// uppercase tracking-wide header, no row-fill, divide-y rows). Same
// dot-status convention for role: bamboo-500 solid for admin, hollow
// zinc ring for member — single accent reserved for"elevated" state.
//
// Admin actions (§4 P2 GDPR Article 17 erasure — #208): an
// inline-confirm "Erase" button appears in the Actions column only
// when the caller is admin AND the target is not the caller. Self-
// erase is blocked server-side too; hiding the button keeps the UI
// honest without a backend round-trip.
export function UsersTable({
 users,
 meId,
 meIsAdmin,
}: {
 users: User[];
 meId: string;
 meIsAdmin: boolean;
}) {
 const t = useTranslations('users');
 if (users.length === 0) {
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
 <th className="px-4 py-3 font-medium">{t('columns.user')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.role')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.provider')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.createdAt')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.lastSeen')}</th>
 {meIsAdmin && <th className="px-4 py-3 font-medium">{t('columns.actions')}</th>}
 </tr>
 </thead>
 <tbody className="divide-y divide-ink-800/70">
 {users.map((u) => (
 <tr key={u.id} className="text-bamboo-100">
 <td className="px-4 py-3">
 <div className="flex items-center gap-3">
 <Avatar email={u.email} />
 <div className="min-w-0">
 <div className="truncate font-medium text-bamboo-50">
 {u.displayName || u.email}
 </div>
 {u.displayName ? (
 <div className="truncate text-xs text-bamboo-200/60">
 {u.email}
 </div>
 ) : null}
 </div>
 </div>
 </td>
 <td className="px-4 py-3">
 <RoleBadge admin={Boolean(u.isAdmin)} />
 </td>
 <td className="px-4 py-3 text-xs text-bamboo-200/60">
 <ProviderLabel provider={u.oidcProvider} />
 </td>
 <td className="px-4 py-3 text-xs text-bamboo-200/60">
 {formatDate(u.createdAt)}
 </td>
 <td className="px-4 py-3 text-xs text-bamboo-200/60">
 {formatRelative(u.updatedAt)}
 </td>
 {meIsAdmin && (
 <td className="px-4 py-3 text-xs">
 {u.id === meId ? (
 <span className="text-bamboo-200/40">{t('actions.selfPlaceholder')}</span>
 ) : (
 <EraseUserButton id={u.id} email={u.email} />
 )}
 </td>
 )}
 </tr>
 ))}
 </tbody>
 </table>
 </div>
 );
}

// EraseUserButton runs the §4 P2 GDPR Article 17 erasure via the
// /api/v1/admin/users/{id}/erase endpoint (#208). Two-step
// confirmation because the operation is irreversible — the
// controller hard-DELETEs the user row + scrubs invitations + writes
// an immutable audit entry. The confirm prompt shows the target
// email so an operator who mis-clicked still has a chance to back
// out.
//
// Errors surface inline next to the button: 400 (self-erase, but
// hidden above so this would only fire if me.id is wrong), 403
// (caller stopped being admin between page load and click — log
// out / log in), 404 (target erased by another admin since this
// list loaded — page refresh shows the change).
function EraseUserButton({ id, email }: { id: string; email: string }) {
 const t = useTranslations('users.actions');
 const [confirming, setConfirming] = useState(false);
 const [error, setError] = useState<string | null>(null);
 const [pending, startTransition] = useTransition();

 if (error) {
 return (
 <div className="space-y-1">
 <span className="text-red-400">{error}</span>
 <button
 type="button"
 onClick={() => setError(null)}
 className="text-bamboo-200/60 underline hover:text-bamboo-50"
 >
 {t('dismiss')}
 </button>
 </div>
 );
 }

 if (confirming) {
 return (
 <div className="flex items-center gap-2">
 <span className="text-amber-400">{t('confirmErase', { email })}</span>
 <button
 type="button"
 disabled={pending}
 onClick={() =>
 startTransition(async () => {
 const res = await eraseUserAction(id);
 if (!res.ok) {
 setError(res.error ?? 'unknown error');
 }
 setConfirming(false);
 })
 }
 className="font-medium text-red-400 underline hover:text-red-300 disabled:opacity-50"
 >
 {pending ? t('working') : t('confirmYes')}
 </button>
 <button
 type="button"
 disabled={pending}
 onClick={() => setConfirming(false)}
 className="text-bamboo-200/60 underline hover:text-bamboo-50 disabled:opacity-50"
 >
 {t('cancel')}
 </button>
 </div>
 );
 }

 return (
 <button
 type="button"
 onClick={() => setConfirming(true)}
 className="text-bamboo-200/60 underline hover:text-red-400"
 >
 {t('erase')}
 </button>
 );
}

function Avatar({ email }: { email: string }) {
 const initial = email[0]?.toUpperCase() ?? '?';
 return (
 <span
 aria-hidden
 className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-ink-800 bg-ink-900 text-xs font-medium text-bamboo-100 dark:bg-ink-900 dark:text-bamboo-100"
 >
 {initial}
 </span>
 );
}

function RoleBadge({ admin }: { admin: boolean }) {
 const t = useTranslations('users.role');
 // Dot + label, same vocabulary as PeerTable status. bamboo-500
 // solid for admin (the brand accent reads as"elevated"), hollow
 // zinc ring for member.
 const dot = admin
 ? 'bg-bamboo-500'
 : 'border border-zinc-400 bg-transparent ';
 return (
 <span className="inline-flex items-center gap-2 text-xs text-bamboo-100">
 <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot}`} />
 {t(admin ? 'admin' : 'member')}
 </span>
 );
}

const KNOWN_PROVIDERS = new Set(['google', 'github']);

function ProviderLabel({ provider }: { provider?: string }) {
 const t = useTranslations('users.provider');
 if (!provider) return <span>—</span>;
 if (KNOWN_PROVIDERS.has(provider)) {
 return <span>{t(provider as never)}</span>;
 }
 // Unknown provider (e.g. dev-fallback users) — show raw string so
 // operators can still see what came through.
 return <span>{provider}</span>;
}

function formatDate(iso: string): string {
 const d = new Date(iso);
 if (Number.isNaN(d.getTime())) return '—';
 return d.toISOString().slice(0, 10);
}

function formatRelative(iso: string): string {
 const ms = Date.now() - new Date(iso).getTime();
 if (!Number.isFinite(ms) || ms < 0) return '—';
 const s = Math.round(ms / 1000);
 if (s < 60) return `${s}s`;
 if (s < 3600) return `${Math.round(s / 60)}m`;
 if (s < 86_400) return `${Math.round(s / 3600)}h`;
 return `${Math.round(s / 86_400)}d`;
}
