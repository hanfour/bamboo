// SPDX-License-Identifier: AGPL-3.0-or-later

import { getTranslations } from 'next-intl/server';

import { Link } from '@/i18n/routing';
import { fetchMe } from '@/lib/api';
import { HeaderNav } from './HeaderNav';

const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';

// Header is the top-bar chrome shared across every page.
//
// Design target: vendor-dashboard (Cloudflare / Google / Apple). Light-
// mode-first, subtle hairline border, sans-serif system stack, one
// muted brand accent (bamboo-500) used sparingly on the brand mark
// and the active-route underline.
//
// Structure: brand · nav · user pill. Server-rendered for the user
// pill's fetchMe() call; HeaderNav is a thin client-only sub-component
// so it can read usePathname for the active-state indicator.
export async function Header() {
  const t = await getTranslations();
  const me = await fetchMe();

  return (
    <header className="sticky top-0 z-30 border-b border-zinc-200 bg-white/95 backdrop-blur supports-[backdrop-filter]:bg-white/80 dark:border-zinc-800 dark:bg-zinc-950/95 dark:supports-[backdrop-filter]:bg-zinc-950/80">
      <div className="mx-auto flex h-14 max-w-7xl items-center gap-8 px-6">
        <Brand />
        <HeaderNav />
        <div className="ml-auto flex items-center gap-3 text-sm">
          {me.authenticated && me.email ? (
            <UserPill email={me.email} tenantSlug={me.tenantSlug} signOutLabel={t('auth.signOut')} />
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

// Brand is the wordmark only — "bamboo" set in Saira Stencil One.
// No accompanying glyph; the various glyph drafts (Mahjong 二條,
// 3×3 nine-grid) all read as either competitor-adjacent or decorative
// noise at header scale. Plain wordmark stays distinctive enough on
// its own thanks to the stencil cuts in the letterforms.
//
// The stencil font is scoped to this one element via the CSS variable
// loaded in [locale]/layout.tsx; the rest of the site stays on the
// system-sans stack.
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

// UserPill replaces the previous "email + sign-out" pair with a
// compact email + tenant + initial-avatar + sign-out. Plain link
// for sign-out — a real dropdown can come later if the menu grows
// beyond one item.
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
