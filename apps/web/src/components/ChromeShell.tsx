// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from 'react';
import { usePathname } from '@/i18n/routing';

// ChromeShell owns the responsive chrome state — specifically the
// drawer-open boolean that the HamburgerButton (in TopBar) sets and
// the Sidebar reacts to.
//
// On lg+ the Sidebar is always-visible as a static rail, so the open
// state is functionally ignored. On <lg the Sidebar renders as a
// fixed slide-in drawer with a backdrop. The shell wraps both TopBar
// and Sidebar so the context provider sits above both consumers.
//
// TopBar stays an async Server Component for fetchMe(); we pass it
// here as a pre-rendered ReactNode prop. Children render unchanged
// inside <main>.
type DrawerCtx = {
  open: boolean;
  setOpen: (open: boolean) => void;
};

const DrawerContext = createContext<DrawerCtx>({
  open: false,
  setOpen: () => {},
});

export function useDrawer(): DrawerCtx {
  return useContext(DrawerContext);
}

export function ChromeShell({
  topBar,
  sidebar,
  children,
}: {
  topBar: ReactNode;
  sidebar: ReactNode;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const pathname = usePathname();

  // Close drawer on route change so navigating to a new page doesn't
  // leave the overlay obscuring the destination.
  useEffect(() => {
    setOpen(false);
  }, [pathname]);

  // ESC closes the drawer. Listener is only armed while open.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open]);

  // Lock body scroll while drawer is open on small viewports — the
  // overlay would otherwise allow the underlying main content to
  // scroll, which feels disconnected. Tailwind: overflow-hidden via
  // direct DOM manipulation because we can't conditionally toggle a
  // body class from inside a child component cleanly.
  useEffect(() => {
    if (!open) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = prev;
    };
  }, [open]);

  return (
    <DrawerContext.Provider value={{ open, setOpen }}>
      {topBar}
      <div className="flex">
        {sidebar}
        <main className="min-w-0 flex-1 px-6 py-8">{children}</main>
      </div>
    </DrawerContext.Provider>
  );
}
