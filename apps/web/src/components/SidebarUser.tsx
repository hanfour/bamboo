// SPDX-License-Identifier: AGPL-3.0-or-later

import { getTranslations } from 'next-intl/server';

import { Link } from '@/i18n/routing';
import { fetchMe } from '@/lib/api';
import { UserMenu } from './UserMenu';

const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';

// SidebarUser is the account block at the bottom of the Sidebar. It
// needs the server-side session, so it is a Server Component that the
// layout passes into the Sidebar as `footer`. Signed-out visitors get a
// Sign in button that opens the sign-in page.
export async function SidebarUser() {
 const t = await getTranslations();
 const me = await fetchMe();

 if (!me.authenticated || !me.email) {
 return (
 <Link
 href="/login"
 className="flex h-8 items-center justify-center rounded-full bg-bamboo-50 text-[13px] font-medium text-ink-950 transition-colors hover:bg-bamboo-100"
 >
 {t('auth.signIn')}
 </Link>
 );
 }

 return (
 <UserMenu
 email={me.email}
 tenantSlug={me.tenantSlug}
 signOutHref={`${CONTROLLER_BASE}/auth/sign-out`}
 labels={{
 menuAria: t('auth.menu.aria'),
 settings: t('auth.menu.settings'),
 signOut: t('auth.signOut'),
 tenant: t('auth.menu.tenant'),
 }}
 />
 );
}
