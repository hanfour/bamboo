// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useEffect, type RefObject } from 'react';

// CSS selector for elements that participate in tab order. Used to
// (a) move focus to the first interactive element on open and
// (b) wrap focus at the edges when the focus trap is enabled.
// Mirrors the conventional modal-a11y recipe; excludes [tabindex=-1]
// which is the "programmatic-focus-only" marker.
const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

type Args = {
  // open mirrors the dialog's visibility. All side effects are
  // gated on this — when false the hook is a no-op.
  open: boolean;
  // onClose fires on ESC. Backdrop click + explicit close buttons
  // remain the caller's responsibility (they have UI context).
  onClose: () => void;
  // panelRef points at the dialog panel DOM node. Focus management
  // and the optional Tab trap both query inside this.
  panelRef: RefObject<HTMLElement | null>;
  // trapTab wraps focus at the first/last focusable inside the
  // panel. The drawer wants this (matches the modal contract its
  // role='dialog' aria-modal='true' declares). Small inline modals
  // (e.g. NewPeerButton) can opt in too.
  trapTab?: boolean;
};

// useDialogA11y wires the three pieces of dialog accessibility every
// modal/drawer in this app wants: body scroll lock, focus-on-open
// (and restore-on-close), and ESC-to-close. Optionally wraps Tab at
// the panel edges.
//
// Extracted from PeerDrawer + NewPeerButton's inline Modal where
// the same logic had landed twice. Keeps both call sites consistent
// and means future a11y improvements (focus visibility, aria-modal
// background inert) need to be made in one place.
export function useDialogA11y({ open, onClose, panelRef, trapTab = false }: Args): void {
  // Focus on open + restore on close. Capture previous focus
  // synchronously when the effect fires; defer the move-into-panel
  // by a microtask so the slide-in / fade transition has started
  // and the panel ref is reachable.
  useEffect(() => {
    if (!open) return;
    const previousFocus = document.activeElement as HTMLElement | null;
    const id = window.setTimeout(() => {
      panelRef.current?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)?.focus();
    }, 0);
    return () => {
      window.clearTimeout(id);
      // Restore focus only when the element is still attached. A
      // delete-then-close flow detaches the originating row before
      // the drawer closes; refocusing a removed node throws on
      // some browsers, so we'd rather hand off to the body fallback.
      if (previousFocus && document.contains(previousFocus)) {
        previousFocus.focus();
      }
    };
  }, [open, panelRef]);

  // Body scroll lock. Stash the previous value so we don't stomp
  // on another component / stylesheet that may have set it.
  useEffect(() => {
    if (!open) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = prev;
    };
  }, [open]);

  // ESC closes; Tab / Shift+Tab wraps focus within the panel when
  // trapTab is enabled. Single keydown listener handles both —
  // cheaper than two effects and keeps the wrap logic next to the
  // ESC dispatch it shares behavioral context with.
  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        onClose();
        return;
      }
      if (!trapTab || e.key !== 'Tab') return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusables = Array.from(
        panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR),
      ).filter((el) => !el.hasAttribute('disabled') && el.tabIndex !== -1);
      if (focusables.length === 0) return;
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose, panelRef, trapTab]);
}
