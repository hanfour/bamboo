// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { Link } from '@/i18n/routing';

export function Header() {
  const t = useTranslations();
  return (
    <header className="border-b border-zinc-200 dark:border-zinc-800">
      <div className="mx-auto flex max-w-6xl items-center justify-between px-6 py-4">
        <Link
          href="/dashboard"
          className="font-mono text-lg font-semibold tracking-tight text-bamboo-600 dark:text-bamboo-400"
        >
          {t('app.name')}
        </Link>
        <nav className="flex gap-6 text-sm">
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
        </nav>
      </div>
    </header>
  );
}
