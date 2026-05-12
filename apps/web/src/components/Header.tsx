// SPDX-License-Identifier: AGPL-3.0-or-later

import { getTranslations } from 'next-intl/server';

import { Link } from '@/i18n/routing';
import { Wordmark } from '@/components/Wordmark';
import { fetchMe } from '@/lib/api';

const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';

// Header is a server component — it reads /me on every render so the
// nav can show the right auth affordance. The visual treatment is a
// thin top "spine" (hairline) over a near-transparent surface; the
// nav links live to the right with kicker-style tracking; the user
// identity chip on the far right shows the tenant + sign-out, or
// nothing-but-a-Sign-in link when unauthenticated.
export async function Header() {
  const t = await getTranslations();
  const me = await fetchMe();

  return (
    <header className="relative">
      {/* top hairline accent — a single bamboo-400 capped rule */}
      <div className="absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-bamboo-400/30 to-transparent" />
      <div className="mx-auto flex max-w-6xl items-center justify-between gap-6 px-6 py-5 sm:px-10">
        <Wordmark />
        <nav className="flex items-center gap-1 text-[13px]">
          <NavLink href="/dashboard">{t('nav.dashboard')}</NavLink>
          <NavLink href="/peers">{t('nav.peers')}</NavLink>
          <NavLink href="/acl">{t('nav.acl')}</NavLink>
          <Divider />
          {me.authenticated && me.email ? (
            <SignedIn
              email={me.email}
              tenantSlug={me.tenantSlug}
              loggedInLabel={t('auth.loggedInAs', { email: me.email })}
              signOutLabel={t('auth.signOut')}
            />
          ) : (
            <a
              href={`${CONTROLLER_BASE}/auth/google/login`}
              className="rounded-md px-3 py-1.5 font-mono text-[11px] uppercase tracking-[0.16em] text-zinc-300 transition hover:bg-white/[0.04] hover:text-zinc-50"
            >
              {t('auth.signIn')}
            </a>
          )}
        </nav>
      </div>
      {/* page-level rule under the header — second part of the editorial double-rule */}
      <div className="mx-auto h-px max-w-6xl bg-white/[0.06]" />
    </header>
  );
}

function NavLink({ href, children }: { href: '/dashboard' | '/peers' | '/acl'; children: React.ReactNode }) {
  return (
    <Link
      href={href}
      className="rounded-md px-3 py-1.5 text-zinc-400 transition hover:bg-white/[0.03] hover:text-zinc-100"
    >
      {children}
    </Link>
  );
}

function Divider() {
  return (
    <span aria-hidden className="mx-2 h-4 w-px self-center bg-white/[0.08]" />
  );
}

function SignedIn({
  email,
  tenantSlug,
  loggedInLabel,
  signOutLabel,
}: {
  email: string;
  tenantSlug: string;
  loggedInLabel: string;
  signOutLabel: string;
}) {
  return (
    <span className="flex items-center gap-3">
      <span
        className="hidden items-center gap-2 text-[11px] sm:flex"
        title={loggedInLabel}
      >
        <span className="font-mono uppercase tracking-[0.16em] text-zinc-500">
          {tenantSlug}
        </span>
        <span className="h-3 w-px bg-white/[0.10]" aria-hidden />
        <span className="text-zinc-300">{email}</span>
      </span>
      <a
        href={`${CONTROLLER_BASE}/auth/sign-out`}
        className="rounded-md px-2 py-1 text-[11px] uppercase tracking-[0.16em] text-zinc-500 transition hover:bg-white/[0.04] hover:text-zinc-200"
      >
        {signOutLabel}
      </a>
    </span>
  );
}
