// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { UsersTable } from '@/components/UsersTable';
import { InvitationsTable } from '@/components/InvitationsTable';
import { InviteUserButton } from '@/components/InviteUserButton';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchInvitations, fetchUsers } from '@/lib/api';
import type { Invitation, User } from '@/lib/types';

// Users + invitations are both admin-only on the wire. We fetch them
// in parallel and treat the users list as primary — if it fails, the
// whole page falls through to FetchErrorState. Invitations failing
// independently is rare in practice (same controller, same auth) but
// we degrade gracefully by passing an empty list rather than blocking
// the working section.
export default async function UsersPage() {
  const [users, invitations] = await Promise.all([fetchUsers(), fetchInvitations()]);
  if (users.kind !== 'ok') {
    return <FetchErrorState kind={users.kind} />;
  }
  return (
    <UsersView
      users={users.value}
      invitations={invitations.kind === 'ok' ? invitations.value : []}
    />
  );
}

function UsersView({
  users,
  invitations,
}: {
  users: User[];
  invitations: Invitation[];
}) {
  const t = useTranslations('users');
  return (
    <div className="space-y-8">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
          <p className="mt-1 max-w-2xl text-sm text-zinc-600 dark:text-zinc-400">
            {t('subtitle')}
          </p>
        </div>
        <div className="shrink-0">
          <InviteUserButton />
        </div>
      </header>

      <section className="space-y-3">
        <UsersTable users={users} />
      </section>

      <section className="space-y-3">
        <h2 className="text-xs font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
          {t('invitations.title')}
        </h2>
        <InvitationsTable invitations={invitations} />
      </section>
    </div>
  );
}
