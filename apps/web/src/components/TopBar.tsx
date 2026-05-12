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
// Brand stays the Saira Stencil "bamboo" wordmark scoped via the
// --font-wordmark CSS variable. User pill stays in the right corner;
// when unauthenticated, the same slot renders an outlined Sign-in
// link to the controller's Google OIDC flow.
export async function TopBar() {
  const t = await getTranslations();
  const me = await fetchMe();

  return (
    <header className="sticky top-0 z-30 h-14 border-b border-zinc-200 bg-white/95 backdrop-blur supports-[backdrop-filter]:bg-white/80 dark:border-zinc-800 dark:bg-zinc-950/95 dark:supports-[backdrop-filter]:bg-zinc-950/80">
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
              className="inline-flex items-center rounded-md border border-zinc-300 px-3 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:border-zinc-400 hover:text-zinc-900 dark:border-zinc-700 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:text-zinc-100"
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
      className="text-[20px] leading-none tracking-tight text-zinc-900 transition-colors hover:text-bamboo-700 dark:text-zinc-100 dark:hover:text-bamboo-400"
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
        <div className="text-xs font-medium leading-tight text-zinc-900 dark:text-zinc-100">
          {email}
        </div>
        <div className="text-[11px] leading-tight text-zinc-500 dark:text-zinc-400">
          {tenantSlug}
        </div>
      </div>
      <span
        aria-hidden
        className="flex h-8 w-8 items-center justify-center rounded-full border border-zinc-200 bg-zinc-50 text-xs font-medium text-zinc-700 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-300"
      >
        {initial}
      </span>
      <a
        href={`${CONTROLLER_BASE}/auth/sign-out`}
        className="text-xs text-zinc-500 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
      >
        {signOutLabel}
      </a>
    </div>
  );
}
