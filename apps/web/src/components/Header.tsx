// SPDX-License-Identifier: AGPL-3.0-or-later

import { getTranslations } from 'next-intl/server';

import { Link } from '@/i18n/routing';
import { fetchMe } from '@/lib/api';

const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';

export async function Header() {
  const t = await getTranslations();
  const me = await fetchMe();

  return (
    <header className="border-b border-zinc-200 dark:border-zinc-800">
      <div className="mx-auto flex max-w-6xl items-center justify-between px-6 py-4">
        <Link
          href="/dashboard"
          className="font-mono text-lg font-semibold tracking-tight text-bamboo-600 dark:text-bamboo-400"
        >
          {t('app.name')}
        </Link>
        <nav className="flex items-center gap-6 text-sm">
          <Link
            className="text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
            href="/dashboard"
          >
            {t('nav.dashboard')}
          </Link>
          <Link
            className="text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
            href="/peers"
          >
            {t('nav.peers')}
          </Link>
          <Link
            className="text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
            href="/acl"
          >
            {t('nav.acl')}
          </Link>
          <span className="h-4 w-px bg-zinc-200 dark:bg-zinc-700" aria-hidden />
          {me.authenticated && me.email ? (
            <>
              <span className="hidden text-xs text-zinc-500 dark:text-zinc-400 sm:inline">
                {t('auth.loggedInAs', { email: me.email })}
              </span>
              <a
                className="text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
                href={`${CONTROLLER_BASE}/auth/sign-out`}
              >
                {t('auth.signOut')}
              </a>
            </>
          ) : (
            <a
              className="text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
              href={`${CONTROLLER_BASE}/auth/google/login`}
            >
              {t('auth.signIn')}
            </a>
          )}
        </nav>
      </div>
    </header>
  );
}
