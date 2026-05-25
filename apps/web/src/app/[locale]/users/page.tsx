// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { UsersTable } from '@/components/UsersTable';
import { InvitationsTable } from '@/components/InvitationsTable';
import { InviteUserButton } from '@/components/InviteUserButton';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchInvitations, fetchMe, fetchUsers } from '@/lib/api';
import type { Invitation, User } from '@/lib/types';

// Users + invitations are both admin-only on the wire. We fetch them
// in parallel and treat the users list as primary — if it fails, the
// whole page falls through to FetchErrorState. Invitations failing
// independently is rare in practice (same controller, same auth) but
// we degrade gracefully by passing an empty list rather than blocking
// the working section.
//
// fetchMe is also pulled so the table can render an "erase" button
// only for admins, and skip the button on the caller's own row
// (self-erase is blocked at the controller; hiding the button keeps
// the UI honest without depending on a backend round-trip).
export default async function UsersPage() {
 const [users, invitations, me] = await Promise.all([
 fetchUsers(),
 fetchInvitations(),
 fetchMe(),
 ]);
 if (users.kind !== 'ok') {
 return <FetchErrorState kind={users.kind} />;
 }
 return (
 <UsersView
 users={users.value}
 invitations={invitations.kind === 'ok' ? invitations.value : []}
 meId={me.userId ?? ''}
 meIsAdmin={Boolean(me.isAdmin)}
 />
 );
}

function UsersView({
 users,
 invitations,
 meId,
 meIsAdmin,
}: {
 users: User[];
 invitations: Invitation[];
 meId: string;
 meIsAdmin: boolean;
}) {
 const t = useTranslations('users');
 return (
 <div className="space-y-8">
 <header className="flex items-start justify-between gap-4">
 <div>
 <h1 className="text-4xl font-light tracking-tight text-bamboo-50 sm:text-5xl">{t("title")}<span className="ml-3 font-serif italic font-normal text-bamboo-300/80">{t("titleAccent")}</span></h1>
 <p className="mt-1 max-w-2xl text-sm text-bamboo-200/70">
 {t('subtitle')}
 </p>
 </div>
 <div className="shrink-0">
 <InviteUserButton />
 </div>
 </header>

 <section className="space-y-3">
 <UsersTable users={users} meId={meId} meIsAdmin={meIsAdmin} />
 </section>

 <section className="space-y-3">
 <h2 className="text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
 {t('invitations.title')}
 </h2>
 <InvitationsTable invitations={invitations} />
 </section>
 </div>
 );
}
