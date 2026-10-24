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
 const hasInvitations = invitations.length > 0;
 return (
 <div className="space-y-8">
 <header className="flex items-start justify-between gap-4">
 <div className="space-y-1.5">
 <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">{t('title')}</h1>
 <p className="max-w-2xl text-[13px] text-bamboo-200/60">{t('subtitle')}</p>
 </div>
 {hasInvitations && (
 <div className="shrink-0">
 <InviteUserButton />
 </div>
 )}
 </header>

 <section className="space-y-3">
 <UsersTable users={users} meId={meId} meIsAdmin={meIsAdmin} />
 </section>

 <section className="space-y-3">
 <h2 className="text-base font-medium text-bamboo-50">{t('invitations.title')}</h2>
 {hasInvitations ? (
 <InvitationsTable invitations={invitations} />
 ) : (
 <div className="flex flex-col items-center rounded-2xl border border-dashed border-[#777]/30 px-6 py-12 text-center">
 <span className="flex h-14 w-14 items-center justify-center rounded-2xl bg-ink-900 text-bamboo-100">
 <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="h-6 w-6" aria-hidden>
 <circle cx="9" cy="8" r="3.5" />
 <path d="M2.5 20c.6-3.4 3.2-5.5 6.5-5.5s5.9 2.1 6.5 5.5" />
 <path d="M18 8v6M15 11h6" />
 </svg>
 </span>
 <h3 className="mt-5 text-[15px] font-medium text-bamboo-50">{t('inviteCard.title')}</h3>
 <p className="mt-2 max-w-md text-[13px] leading-5 text-bamboo-200/60">{t('inviteCard.desc')}</p>
 <div className="mt-6">
 <InviteUserButton />
 </div>
 </div>
 )}
 </section>
 </div>
 );
}
