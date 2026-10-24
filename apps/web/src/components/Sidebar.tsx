// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import type { ReactNode } from 'react';
import { useTranslations } from 'next-intl';
import { Link, usePathname } from '@/i18n/routing';
import { useDrawer } from './ChromeShell';

// Sidebar is the persistent left rail on lg+ and a slide-in drawer below.
//
// Layout (2026-10-24, after the Cursor dashboard): a full-height #111
// column. The brand sits on top, the links come in labelled groups, and
// the signed-in user sits at the bottom (passed in as `footer`, because
// it needs the server-side session). The active row is a rounded,
// translucent grey fill. Below lg the same column is a fixed drawer that
// the top bar's menu button opens over a dimmed backdrop.
type Href = '/' | '/peers' | '/users' | '/acl' | '/dns' | '/logs' | '/settings';
type Item = { href: Href; label: string; icon: ReactNode };

export function Sidebar({ footer }: { footer?: ReactNode }) {
 const t = useTranslations('nav');
 const pathname = usePathname();
 const { open, setOpen } = useDrawer();

 const groups: Array<{ label?: string; items: Item[] }> = [
 { items: [{ href: '/', label: t('dashboard'), icon: <HomeIcon /> }] },
 {
 label: t('groupNetwork'),
 items: [
 { href: '/peers', label: t('peers'), icon: <ServerIcon /> },
 { href: '/users', label: t('users'), icon: <UsersIcon /> },
 { href: '/acl', label: t('acl'), icon: <ShieldIcon /> },
 { href: '/dns', label: t('dns'), icon: <GlobeIcon /> },
 ],
 },
 {
 label: t('groupSystem'),
 items: [
 { href: '/logs', label: t('logs'), icon: <LogsIcon /> },
 // Pre-auth keys, API tokens and webhooks are reached from Settings.
 { href: '/settings', label: t('settings'), icon: <GearIcon /> },
 ],
 },
 ];

 return (
 <>
 <div
 aria-hidden
 onClick={() => setOpen(false)}
 className={[
 'fixed inset-0 z-20 bg-black/70 transition-opacity lg:hidden',
 open ? 'opacity-100' : 'pointer-events-none opacity-0',
 ].join(' ')}
 />
 <aside
 id="primary-sidebar"
 className={[
 'flex h-screen w-64 shrink-0 flex-col bg-ink-900 px-3 pb-3 pt-4',
 'fixed left-0 top-0 z-30 transition-transform',
 open ? 'translate-x-0' : '-translate-x-full',
 'lg:sticky lg:z-0 lg:translate-x-0',
 ].join(' ')}
 >
 <Link
 href="/"
 className="mb-4 px-2.5 text-[17px] font-semibold leading-none tracking-tight text-bamboo-50"
 >
 bamboo
 </Link>
 <nav className="min-h-0 flex-1 space-y-5 overflow-y-auto">
 {groups.map((g, i) => (
 <div key={i}>
 {g.label && <div className="px-2.5 pb-1.5 text-xs text-bamboo-200/50">{g.label}</div>}
 <ul className="space-y-0.5">
 {g.items.map((it) => {
 const active = isActive(pathname, it.href);
 return (
 <li key={it.href}>
 <Link
 href={it.href}
 aria-current={active ? 'page' : undefined}
 className={[
 'flex items-center gap-3 rounded-lg px-2.5 py-1.5 text-[13px] leading-[18px] transition-colors',
 active
 ? 'bg-[#777]/30 font-medium text-bamboo-50'
 : 'text-bamboo-200/80 hover:bg-[#777]/20 hover:text-bamboo-50',
 ].join(' ')}
 >
 <span aria-hidden className={active ? 'text-bamboo-50' : 'text-bamboo-300/80'}>
 {it.icon}
 </span>
 <span className="truncate">{it.label}</span>
 </Link>
 </li>
 );
 })}
 </ul>
 </div>
 ))}
 </nav>
 {footer && <div className="mt-3 border-t border-[#777]/20 pt-3">{footer}</div>}
 </aside>
 </>
 );
}

function isActive(pathname: string, href: string): boolean {
 if (href === '/') return pathname === '/';
 // Treat /peers/anything as the peers entry being active. Same for
 // /preauth-keys and /acl. Keeps the active pill stable across
 // sub-routes and search-param-only navigations.
 return pathname === href || pathname.startsWith(href + '/');
}

// Outlined icons (stroke-only, 1.5px). Sized via h-5 w-5 inline so the
// active/inactive color comes from currentColor on the wrapping <span>.

function HomeIcon() {
 return (
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 >
 <path d="M3 12 12 4l9 8" />
 <path d="M5 10v10h14V10" />
 <path d="M10 20v-6h4v6" />
 </svg>
 );
}

function ServerIcon() {
 return (
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 >
 <rect x="3" y="4" width="18" height="6" rx="1" />
 <rect x="3" y="14" width="18" height="6" rx="1" />
 <path d="M7 7h.01" />
 <path d="M7 17h.01" />
 </svg>
 );
}

function UsersIcon() {
 return (
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 >
 <circle cx="9" cy="8" r="3.5" />
 <path d="M2.5 20a6.5 6.5 0 0 1 13 0" />
 <path d="M17 11a3 3 0 1 0 0-6" />
 <path d="M21.5 20a5.5 5.5 0 0 0-4.5-5.4" />
 </svg>
 );
}

function GlobeIcon() {
 return (
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 >
 <circle cx="12" cy="12" r="9" />
 <path d="M3 12h18" />
 <path d="M12 3a14 14 0 0 1 0 18" />
 <path d="M12 3a14 14 0 0 0 0 18" />
 </svg>
 );
}

function GearIcon() {
 return (
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 >
 <circle cx="12" cy="12" r="3" />
 <path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3h.1a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8v.1a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1Z" />
 </svg>
 );
}

function LogsIcon() {
 // Document outline with three horizontal text lines — line-art so it
 // sits in the same 1.5px stroke family as the other rail icons.
 // Reads as"log entries / a stream of records" without conflicting
 // with ShieldIcon (ACL) or ServerIcon (Peers).
 return (
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 >
 <path d="M6 3h9l4 4v14a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1Z" />
 <path d="M14 3v5h5" />
 <path d="M8 13h8" />
 <path d="M8 17h8" />
 <path d="M8 9h3" />
 </svg>
 );
}

function ShieldIcon() {
 return (
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 >
 <path d="M12 3 5 6v6c0 4.5 3 8 7 9 4-1 7-4.5 7-9V6l-7-3Z" />
 </svg>
 );
}
