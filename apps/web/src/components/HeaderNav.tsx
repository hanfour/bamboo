// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useTranslations } from 'next-intl';
import { Link, usePathname } from '@/i18n/routing';

// HeaderNav renders the top-bar route links with an active state.
// Split into a client component so the parent Header can stay server-
// rendered (it does a server-side fetchMe() for the user pill).
//
// Active detection: exact match on /, else hasPrefix match. /peers
// stays "active" when the URL is /peers?selected=<id> or any future
// /peers/<sub> route.
export function HeaderNav() {
  const t = useTranslations('nav');
  const pathname = usePathname();

  const items: Array<{ href: '/' | '/peers' | '/preauth-keys' | '/acl'; label: string }> = [
    { href: '/', label: t('dashboard') },
    { href: '/peers', label: t('peers') },
    { href: '/preauth-keys', label: t('preAuthKeys') },
    { href: '/acl', label: t('acl') },
  ];

  return (
    <nav className="flex items-center gap-1 text-sm">
      {items.map((it) => {
        const active = isActive(pathname, it.href);
        return (
          <Link
            key={it.href}
            href={it.href}
            aria-current={active ? 'page' : undefined}
            className={[
              'relative px-3 py-4 transition-colors',
              active
                ? 'text-zinc-900 dark:text-zinc-100'
                : 'text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100',
            ].join(' ')}
          >
            {it.label}
            {active && (
              <span
                aria-hidden
                className="absolute inset-x-3 -bottom-px h-0.5 rounded-full bg-bamboo-500"
              />
            )}
          </Link>
        );
      })}
    </nav>
  );
}

function isActive(pathname: string, href: string): boolean {
  if (href === '/') return pathname === '/';
  // Treat /peers/anything as the peers tab being active. Same for
  // /preauth-keys and /acl. Keeps the active indicator stable across
  // sub-routes and search-param-only navigations.
  return pathname === href || pathname.startsWith(href + '/');
}
