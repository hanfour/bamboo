// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useEffect, useRef, useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { deletePeerAction, setPeerStatusAction } from '@/lib/actions';
import type { Peer } from '@/lib/types';

// PeerRowMenu is the kebab-button row action surface introduced as
// part of the §6 polish pass. The same Disable / Enable / Delete
// affordances live inside the PeerDrawer; this menu lifts the
// commonly-used ones to the row so power users don't have to open
// the drawer first.
//
// Visual + interaction parity with UserMenu (popover): click toggles,
// click-outside + Escape close, items are plain buttons. Click on
// the kebab itself swallows the event so the surrounding row's
// onSelect doesn't also fire.
export function PeerRowMenu({ peer }: { peer: Peer }) {
 const t = useTranslations('peers.rowMenu');
 const [open, setOpen] = useState(false);
 const [error, setError] = useState<string | null>(null);
 const [confirmDelete, setConfirmDelete] = useState(false);
 const [pending, startTransition] = useTransition();
 const wrapperRef = useRef<HTMLDivElement>(null);

 const isDisabled = peer.status === 'disabled';

 useEffect(() => {
 if (!open) return;
 function onClickOutside(e: MouseEvent) {
 if (wrapperRef.current && !wrapperRef.current.contains(e.target as Node)) {
 setOpen(false);
 setConfirmDelete(false);
 setError(null);
 }
 }
 function onKey(e: KeyboardEvent) {
 if (e.key === 'Escape') {
 setOpen(false);
 setConfirmDelete(false);
 setError(null);
 }
 }
 document.addEventListener('mousedown', onClickOutside);
 document.addEventListener('keydown', onKey);
 return () => {
 document.removeEventListener('mousedown', onClickOutside);
 document.removeEventListener('keydown', onKey);
 };
 }, [open]);

 function runToggleStatus(e: React.MouseEvent) {
 e.stopPropagation();
 setError(null);
 startTransition(async () => {
 const next = isDisabled ? 'online' : 'disabled';
 const res = await setPeerStatusAction(peer.id, next);
 if (res.ok) {
 setOpen(false);
 } else {
 setError(res.error);
 }
 });
 }

 function runDelete(e: React.MouseEvent) {
 e.stopPropagation();
 setError(null);
 startTransition(async () => {
 const res = await deletePeerAction(peer.id);
 if (res.ok) {
 setOpen(false);
 setConfirmDelete(false);
 } else {
 setError(res.error);
 }
 });
 }

 return (
 <div ref={wrapperRef} className="relative inline-block">
 <button
 type="button"
 onClick={(e) => {
 e.stopPropagation();
 setOpen((v) => !v);
 setConfirmDelete(false);
 setError(null);
 }}
 onKeyDown={(e) => {
 // Prevent row's keyboard handler (Enter/Space → onSelect)
 // from swallowing the kebab toggle.
 if (e.key === 'Enter' || e.key === ' ') {
 e.stopPropagation();
 }
 }}
 aria-haspopup="menu"
 aria-expanded={open}
 aria-label={t('aria')}
 className="inline-flex h-6 w-6 items-center justify-center rounded text-bamboo-200/60 transition-colors hover:bg-ink-800 hover:text-bamboo-50 focus:outline-none focus-visible:ring-1 focus-visible:ring-bamboo-400"
 >
 <svg
 viewBox="0 0 20 20"
 fill="currentColor"
 aria-hidden
 className="h-4 w-4"
 >
 <circle cx="10" cy="4" r="1.5" />
 <circle cx="10" cy="10" r="1.5" />
 <circle cx="10" cy="16" r="1.5" />
 </svg>
 </button>
 {open && (
 <div
 role="menu"
 // right-0 anchors to the right edge of the kebab so the
 // popover doesn't overflow the table on the actions column.
 className="absolute right-0 top-full z-40 mt-1 w-48 overflow-hidden rounded-md border border-ink-800 bg-ink-950 shadow-lg shadow-ink-950/40"
 onClick={(e) => e.stopPropagation()}
 >
 <ul className="py-1 text-sm">
 <li>
 <button
 type="button"
 role="menuitem"
 onClick={runToggleStatus}
 disabled={pending}
 className="block w-full px-3 py-2 text-left text-bamboo-100 transition-colors hover:bg-ink-900 hover:text-bamboo-50 disabled:opacity-50"
 >
 {pending && !confirmDelete
 ? t('working')
 : isDisabled
 ? t('enable')
 : t('disable')}
 </button>
 </li>
 <li>
 {confirmDelete ? (
 <div className="space-y-1 px-3 py-2">
 <p className="text-xs text-red-400">{t('confirmDelete')}</p>
 <div className="flex gap-2">
 <button
 type="button"
 onClick={runDelete}
 disabled={pending}
 className="flex-1 rounded border border-red-900/50 bg-red-900/20 px-2 py-1 text-xs font-medium text-red-300 transition-colors hover:bg-red-900/30 disabled:opacity-50"
 >
 {pending ? t('working') : t('confirmYes')}
 </button>
 <button
 type="button"
 onClick={(e) => {
 e.stopPropagation();
 setConfirmDelete(false);
 setError(null);
 }}
 disabled={pending}
 className="flex-1 rounded border border-ink-700 px-2 py-1 text-xs text-bamboo-200/80 transition-colors hover:bg-ink-800 hover:text-bamboo-50 disabled:opacity-50"
 >
 {t('confirmCancel')}
 </button>
 </div>
 </div>
 ) : (
 <button
 type="button"
 role="menuitem"
 onClick={(e) => {
 e.stopPropagation();
 setConfirmDelete(true);
 setError(null);
 }}
 disabled={pending}
 className="block w-full px-3 py-2 text-left text-bamboo-100 transition-colors hover:bg-red-900/20 hover:text-red-300 disabled:opacity-50"
 >
 {t('delete')}
 </button>
 )}
 </li>
 </ul>
 {error && (
 <div
 role="alert"
 className="border-t border-ink-800 bg-red-900/15 px-3 py-2 text-xs text-red-300"
 >
 {t('errorPrefix')} {error}
 </div>
 )}
 </div>
 )}
 </div>
 );
}
