// SPDX-License-Identifier: AGPL-3.0-or-later

import { getTranslations } from 'next-intl/server';

import { Link } from '@/i18n/routing';
import { fetchMe } from '@/lib/api';
import { HamburgerButton } from './HamburgerButton';

const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';

// TopBar is the thin chrome at the very top of every page.
//
// Design target: Google Account / Workspace Admin / GCP Console —
// minimal top bar (brand + user only, no nav) paired with a left
// sidebar (Sidebar.tsx) below. Differentiates from Tailscale's
// horizontal-tab top bar so we don't read as a clone.
//
// Brand stays the Saira Stencil"bamboo" wordmark scoped via the
// --font-wordmark CSS variable. User pill stays in the right corner;
// when unauthenticated, the same slot renders an outlined Sign-in
// link to the controller's Google OIDC flow.
export async function TopBar() {
 const t = await getTranslations();
 const me = await fetchMe();

 return (
 <header className="sticky top-0 z-30 h-14 border-b border-ink-800 bg-ink-950/95 backdrop-blur supports-[backdrop-filter]:bg-ink-950/80">
 <div className="flex h-full items-center justify-between px-6">
 <div className="flex items-center gap-3">
 <HamburgerButton />
 <Brand />
 </div>
 <div className="flex items-center gap-3 text-sm">
 {me.authenticated && me.email ? (
 <UserPill
 email={me.email}
 tenantSlug={me.tenantSlug}
 signOutLabel={t('auth.signOut')}
 />
 ) : (
 <a
 href={`${CONTROLLER_BASE}/auth/google/login`}
 className="inline-flex items-center rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm font-light text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50"
 >
 {t('auth.signIn')}
 </a>
 )}
 </div>
 </div>
 </header>
 );
}

function Brand() {
 return (
 <Link
 href="/"
 className="text-[20px] leading-none tracking-tight text-bamboo-50 transition-colors hover:text-bamboo-300"
 style={{ fontFamily: 'var(--font-wordmark), system-ui, sans-serif' }}
 >
 bamboo
 </Link>
 );
}

function UserPill({
 email,
 tenantSlug,
 signOutLabel,
}: {
 email: string;
 tenantSlug: string;
 signOutLabel: string;
}) {
 const initial = email[0]?.toUpperCase() ?? '?';
 return (
 <div className="flex items-center gap-3">
 <div className="hidden text-right sm:block">
 <div className="text-xs font-medium leading-tight text-bamboo-50">
 {email}
 </div>
 <div className="text-[11px] leading-tight text-bamboo-200/60">
 {tenantSlug}
 </div>
 </div>
 <span
 aria-hidden
 className="flex h-8 w-8 items-center justify-center rounded-full border border-bamboo-200/30 bg-ink-900 text-xs font-medium text-bamboo-100"
 >
 {initial}
 </span>
 <a
 href={`${CONTROLLER_BASE}/auth/sign-out`}
 className="text-xs text-bamboo-200/60 transition-colors hover:text-bamboo-50"
 >
 {signOutLabel}
 </a>
 </div>
 );
}
