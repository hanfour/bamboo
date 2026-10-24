// SPDX-License-Identifier: AGPL-3.0-or-later

import { Link } from '@/i18n/routing';
import { HamburgerButton } from './HamburgerButton';

// TopBar is the slim bar shown below lg only. On lg and wider the
// Sidebar carries the brand and the signed-in user, so this bar would
// repeat them. Here it holds the menu button that opens the Sidebar
// drawer, plus the brand.
export function TopBar() {
 return (
 <header className="sticky top-0 z-10 flex h-14 items-center gap-3 bg-ink-950/90 px-4 backdrop-blur lg:hidden">
 <HamburgerButton />
 <Link
 href="/"
 className="text-[17px] font-semibold leading-none tracking-tight text-bamboo-50"
 >
 bamboo
 </Link>
 </header>
 );
}
