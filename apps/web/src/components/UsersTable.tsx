// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useTranslations } from 'next-intl';
import type { User } from '@/lib/types';

// UsersTable mirrors PeerTable's visual vocabulary (hairline border,
// uppercase tracking-wide header, no row-fill, divide-y rows). Same
// dot-status convention for role: bamboo-500 solid for admin, hollow
// zinc ring for member — single accent reserved for "elevated" state.
//
// No row click target yet — user-detail drawer / role-change actions
// land in a follow-up PR once invite + role-update endpoints are
// designed.
export function UsersTable({ users }: { users: User[] }) {
  const t = useTranslations('users');
  if (users.length === 0) {
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
            <th className="px-4 py-3 font-medium">{t('columns.user')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.role')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.provider')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.createdAt')}</th>
            <th className="px-4 py-3 font-medium">{t('columns.lastSeen')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-200 dark:divide-zinc-800">
          {users.map((u) => (
            <tr key={u.id} className="text-zinc-700 dark:text-zinc-300">
              <td className="px-4 py-3">
                <div className="flex items-center gap-3">
                  <Avatar email={u.email} />
                  <div className="min-w-0">
                    <div className="truncate font-medium text-zinc-900 dark:text-zinc-100">
                      {u.displayName || u.email}
                    </div>
                    {u.displayName ? (
                      <div className="truncate text-xs text-zinc-500 dark:text-zinc-400">
                        {u.email}
                      </div>
                    ) : null}
                  </div>
                </div>
              </td>
              <td className="px-4 py-3">
                <RoleBadge admin={Boolean(u.isAdmin)} />
              </td>
              <td className="px-4 py-3 text-xs text-zinc-500 dark:text-zinc-400">
                <ProviderLabel provider={u.oidcProvider} />
              </td>
              <td className="px-4 py-3 text-xs text-zinc-500 dark:text-zinc-400">
                {formatDate(u.createdAt)}
              </td>
              <td className="px-4 py-3 text-xs text-zinc-500 dark:text-zinc-400">
                {formatRelative(u.updatedAt)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Avatar({ email }: { email: string }) {
  const initial = email[0]?.toUpperCase() ?? '?';
  return (
    <span
      aria-hidden
      className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-zinc-200 bg-zinc-50 text-xs font-medium text-zinc-700 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-300"
    >
      {initial}
    </span>
  );
}

function RoleBadge({ admin }: { admin: boolean }) {
  const t = useTranslations('users.role');
  // Dot + label, same vocabulary as PeerTable status. bamboo-500
  // solid for admin (the brand accent reads as "elevated"), hollow
  // zinc ring for member.
  const dot = admin
    ? 'bg-bamboo-500'
    : 'border border-zinc-400 bg-transparent dark:border-zinc-500';
  return (
    <span className="inline-flex items-center gap-2 text-xs text-zinc-700 dark:text-zinc-300">
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
