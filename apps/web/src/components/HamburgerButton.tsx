// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useDrawer } from './ChromeShell';

// HamburgerButton toggles the Sidebar drawer on small viewports.
// Hidden on lg+ where the Sidebar is always-visible as a static rail.
//
// Renders an outlined icon that swaps between three-bar (closed) and
// X (open). Lives in the TopBar's left slot so the icon sits to the
// left of the brand wordmark.
export function HamburgerButton() {
 const { open, setOpen } = useDrawer();
 return (
 <button
 type="button"
 onClick={() => setOpen(!open)}
 aria-label="Toggle navigation"
 aria-expanded={open}
 aria-controls="primary-sidebar"
 className="-ml-2 inline-flex h-9 w-9 items-center justify-center rounded-md text-bamboo-200/70 transition-colors hover:bg-ink-900 hover:text-bamboo-50 lg:hidden"
 >
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 className="h-5 w-5"
 aria-hidden
 >
 {open ? (
 <>
 <path d="M6 6l12 12" />
 <path d="M18 6L6 18" />
 </>
 ) : (
 <>
 <path d="M4 6h16" />
 <path d="M4 12h16" />
 <path d="M4 18h16" />
 </>
 )}
 </svg>
 </button>
 );
}
