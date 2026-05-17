// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useTranslations } from 'next-intl';
import { Link, usePathname } from '@/i18n/routing';
import { useDrawer } from './ChromeShell';

// Sidebar is the persistent left navigation rail on lg+ viewports and
// a slide-in drawer below lg.
//
// Design (committed 2026-05-17): warm-dark surface (ink-950), active
// state shown by a 2px bamboo-400 left bar + bamboo-50 text — no
// background fill, no pill. Inspired by Linear's nav (no filled
// active pill, just typography weight + color shift). The icon
// dims/brightens to track.
//
// Responsive behavior:
// - lg+ (≥1024): always-visible static rail inside the flex flow.
// Takes 240px of main-axis space; transparent bg so it sits on
// the page surface.
// - <lg: fixed-position drawer that slides in from the left over
// an ink-950/80 backdrop. Drawer open state lives in ChromeShell
// context; the HamburgerButton in TopBar toggles it.
export function Sidebar() {
 const t = useTranslations('nav');
 const pathname = usePathname();
 const { open, setOpen } = useDrawer();

 const items: Array<{
 href: '/' | '/peers' | '/users' | '/acl' | '/dns' | '/logs' | '/settings';
 label: string;
 icon: React.ReactNode;
 }> = [
 { href: '/', label: t('dashboard'), icon: <HomeIcon /> },
 { href: '/peers', label: t('peers'), icon: <ServerIcon /> },
 { href: '/users', label: t('users'), icon: <UsersIcon /> },
 { href: '/acl', label: t('acl'), icon: <ShieldIcon /> },
 { href: '/dns', label: t('dns'), icon: <GlobeIcon /> },
 { href: '/logs', label: t('logs'), icon: <LogsIcon /> },
 // Pre-auth keys lives under Settings now (matches Tailscale's IA);
 // /preauth-keys route still exists, just reached via Settings →
 // Pre-auth keys card.
 { href: '/settings', label: t('settings'), icon: <GearIcon /> },
 ];

 return (
 <>
 {/* Backdrop — only rendered under lg. fade-in animation rather
 than appear/disappear. Warm-dark scrim, not zinc grey. */}
 <div
 aria-hidden
 onClick={() => setOpen(false)}
 className={[
 'fixed inset-x-0 bottom-0 top-14 z-20 bg-ink-950/80 transition-opacity lg:hidden',
 open ? 'opacity-100' : 'pointer-events-none opacity-0',
 ].join(' ')}
 />
 <aside
 id="primary-sidebar"
 className={[
 // Shared chrome: ink-800 hairline on the right, generous
 // vertical breathing room.
 'h-[calc(100vh-3.5rem)] w-60 shrink-0 overflow-y-auto border-r border-ink-800 px-3 py-4',
 // <lg: fixed drawer that slides in. ink-900 raises it one
 // step above the page surface so the backdrop reads
 // distinctly.
 'fixed left-0 top-14 z-30 bg-ink-900 transition-transform',
 open ? 'translate-x-0' : '-translate-x-full',
 // lg+: static rail inside flex flow, transparent so it
 // shares the page surface.
 'lg:sticky lg:top-14 lg:z-0 lg:translate-x-0 lg:bg-transparent',
 ].join(' ')}
 >
 <nav>
 <ul className="space-y-0.5">
 {items.map((it) => {
 const active = isActive(pathname, it.href);
 return (
 <li key={it.href}>
 <Link
 href={it.href}
 aria-current={active ? 'page' : undefined}
 className={[
 // Active state = 2px bamboo-400 left bar via
 // `before:`, no background pill. Inactive
 // rows hover into an ink-900 fill for
 // affordance without committing to the pill
 // look.
 'relative flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
 active
 ? 'font-medium text-bamboo-50 before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-bamboo-400'
 : 'text-bamboo-200/80 hover:bg-ink-900 hover:text-bamboo-50',
 ].join(' ')}
 >
 <span
 aria-hidden
 className={active ? 'text-bamboo-300' : 'text-bamboo-400/70'}
 >
 {it.icon}
 </span>
 <span className="truncate">{it.label}</span>
 </Link>
 </li>
 );
 })}
 </ul>
 </nav>
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
