// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useEffect, useRef, useState } from 'react';
import { Link } from '@/i18n/routing';

// UserMenu replaces the inline email + sign-out chip the TopBar used
// to render. Same visual vocabulary as Google / Vercel / Linear: the
// top bar shows just the avatar circle; clicking opens a popover
// with the identity header + a Settings shortcut + sign-out.
//
// The popover handles its own dismissal — click outside or Escape
// closes it, the avatar button toggles. Focus is not trapped
// (low-stakes menu, two items); pressing Tab cycles through them
// naturally.
export function UserMenu({
 email,
 tenantSlug,
 signOutHref,
 labels,
}: {
 email: string;
 tenantSlug: string;
 signOutHref: string;
 labels: {
 menuAria: string;
 settings: string;
 signOut: string;
 tenant: string;
 };
}) {
 const [open, setOpen] = useState(false);
 const wrapperRef = useRef<HTMLDivElement>(null);
 const initial = email[0]?.toUpperCase() ?? '?';

 useEffect(() => {
 if (!open) return;
 function onClickOutside(e: MouseEvent) {
 if (wrapperRef.current && !wrapperRef.current.contains(e.target as Node)) {
 setOpen(false);
 }
 }
 function onKey(e: KeyboardEvent) {
 if (e.key === 'Escape') setOpen(false);
 }
 document.addEventListener('mousedown', onClickOutside);
 document.addEventListener('keydown', onKey);
 return () => {
 document.removeEventListener('mousedown', onClickOutside);
 document.removeEventListener('keydown', onKey);
 };
 }, [open]);

 return (
 <div ref={wrapperRef} className="relative">
 <button
 type="button"
 onClick={() => setOpen((v) => !v)}
 aria-haspopup="menu"
 aria-expanded={open}
 aria-label={labels.menuAria}
 className="flex h-8 w-8 items-center justify-center rounded-full border border-bamboo-200/30 bg-ink-900 text-xs font-medium text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50"
 >
 {initial}
 </button>
 {open && (
 <div
 role="menu"
 className="absolute right-0 top-full z-40 mt-2 w-64 overflow-hidden rounded-lg border border-ink-800 bg-ink-950 shadow-lg shadow-ink-950/40"
 >
 <header className="space-y-0.5 border-b border-ink-800 px-3 py-2.5">
 <div className="truncate text-sm font-medium text-bamboo-50">
 {email}
 </div>
 <div className="truncate text-[11px] text-bamboo-200/60">
 {labels.tenant}: {tenantSlug}
 </div>
 </header>
 <ul className="py-1 text-sm">
 <li>
 <Link
 href="/settings"
 role="menuitem"
 onClick={() => setOpen(false)}
 className="block px-3 py-2 text-bamboo-100 transition-colors hover:bg-ink-900 hover:text-bamboo-50"
 >
 {labels.settings}
 </Link>
 </li>
 <li>
 <a
 href={signOutHref}
 role="menuitem"
 className="block px-3 py-2 text-bamboo-100 transition-colors hover:bg-ink-900 hover:text-bamboo-50"
 >
 {labels.signOut}
 </a>
 </li>
 </ul>
 </div>
 )}
 </div>
 );
}
