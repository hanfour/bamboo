// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useEffect, useRef, useState } from 'react';
import { Link } from '@/i18n/routing';
import { ThemeToggle } from './ThemeToggle';

// UserMenu is the account block at the bottom of the Sidebar: avatar,
// email and tenant, with a popover that opens upward for Settings and
// sign-out.
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
 className="flex w-full items-center gap-2.5 rounded-lg px-2 py-1.5 text-left transition-colors hover:bg-[#777]/20"
 >
 <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-ink-800 text-xs font-medium text-bamboo-50">
 {initial}
 </span>
 <span className="min-w-0 flex-1">
 <span className="block truncate text-[13px] leading-[18px] text-bamboo-50">{email}</span>
 <span className="block truncate text-[11px] leading-4 text-bamboo-200/60">{tenantSlug}</span>
 </span>
 <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4 shrink-0 text-bamboo-300/80" aria-hidden>
 <path d="M8 9l4-4 4 4" />
 <path d="M8 15l4 4 4-4" />
 </svg>
 </button>
 {open && (
 <div
 role="menu"
 className="absolute inset-x-0 bottom-full z-40 mb-2 overflow-hidden rounded-xl bg-ink-850 p-1 shadow-xl ring-1 ring-[#777]/30"
 >
 <div className="px-3 py-2 text-[11px] text-bamboo-200/60">
 {labels.tenant}: {tenantSlug}
 </div>
 <div className="px-3 py-2">
 <ThemeToggle />
 </div>
 <Link
 href="/settings"
 role="menuitem"
 onClick={() => setOpen(false)}
 className="block rounded-lg px-3 py-2 text-[13px] text-bamboo-100 transition-colors hover:bg-[#777]/20 hover:text-bamboo-50"
 >
 {labels.settings}
 </Link>
 <a
 href={signOutHref}
 role="menuitem"
 className="block rounded-lg px-3 py-2 text-[13px] text-bamboo-100 transition-colors hover:bg-[#777]/20 hover:text-bamboo-50"
 >
 {labels.signOut}
 </a>
 </div>
 )}
 </div>
 );
}
