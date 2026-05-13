// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { UsersTable } from '@/components/UsersTable';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchUsers } from '@/lib/api';
import type { User } from '@/lib/types';

// Users is admin-only on the wire (GET /api/v1/users returns 403 for
// member role), so the page renders the forbidden FetchErrorState
// variant for non-admins. Invite + role-change + delete flows are
// out of scope for this Phase B skeleton — they need email config +
// audit shape decisions first.
export default async function UsersPage() {
  const users = await fetchUsers();
  if (users.kind !== 'ok') {
    return <FetchErrorState kind={users.kind} />;
  }
  return <UsersView users={users.value} />;
}

function UsersView({ users }: { users: User[] }) {
  const t = useTranslations('users');
  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
        <p className="mt-1 max-w-2xl text-sm text-zinc-600 dark:text-zinc-400">
          {t('subtitle')}
        </p>
      </header>
      <UsersTable users={users} />
    </div>
  );
}
