// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import type { Peer } from '@/lib/types';

type Props = {
 peers: Peer[];
 selectedId?: string;
 onSelect: (id: string) => void;
};

// PeerTable renders the tenant's peer list as a borderless table on
// the warm-dark surface. Visual rules committed 2026-05-17:
//
// - No outer card border. The table sits flush on the page; the
// ink-800 hairlines between rows carry the structure.
// - Selected row gets a 2px bamboo-400 left stripe (same accent
// the Sidebar uses for the active nav item) plus an ink-900
// fill to read as raised, without committing to a pill.
// - DNS-name column lives between hostname and IP. The full
// resolvable form (`<peerDnsName>.bamboo`) renders mono with
// the `.bamboo` suffix dimmed so the eye lands on the label.
// - Tags are borderless ghost text — Tailscale-style underlined
// chips would compete with row hover, and Muji-leaning means
// less chrome.
export function PeerTable({ peers, selectedId, onSelect }: Props) {
 const t = useTranslations('peers');
 const [expandedId, setExpandedId] = useState<string | null>(null);

 if (peers.length === 0) {
 return (
 <p className="rounded-md border border-dashed border-ink-700 p-10 text-center text-sm text-bamboo-200/60">
 {t('empty')}
 </p>
 );
 }

 return (
 // -mx-6 sm:mx-0 lets the table edge-to-edge on phones so the
 // horizontal scrollbar overlay isn't clipped by the page
 // padding. min-w-[860px] keeps columns at readable widths;
 // pinching/scrolling the table beats fortune-cookie wrapping.
 <div className="-mx-6 overflow-x-auto px-6 sm:mx-0 sm:px-0">
 <table className="w-full min-w-[860px] text-sm">
 <thead className="border-b border-ink-800 text-left text-xs font-medium tracking-wide text-bamboo-200/60">
 <tr>
 <th className="px-3 py-3 font-medium">{t('columns.hostname')}</th>
 <th className="px-3 py-3 font-medium">{t('columns.dnsName')}</th>
 <th className="px-3 py-3 font-medium">{t('columns.ip')}</th>
 <th className="px-3 py-3 font-medium">{t('columns.owner')}</th>
 <th className="px-3 py-3 font-medium">{t('columns.tags')}</th>
 <th className="px-3 py-3 font-medium">{t('columns.os')}</th>
 <th className="px-3 py-3 font-medium">{t('columns.status')}</th>
 <th className="px-3 py-3 font-medium">{t('columns.lastSeen')}</th>
 </tr>
 </thead>
 <tbody className="divide-y divide-ink-800/70">
 {peers.map((p) => {
 const isSelected = p.id === selectedId;
 const isExpanded = expandedId === p.id;
 return (
 <tr
 key={p.id}
 onClick={() => onSelect(p.id)}
 onKeyDown={(e) => {
 if (e.key === 'Enter' || e.key === ' ') {
 e.preventDefault();
 onSelect(p.id);
 }
 }}
 tabIndex={0}
 role="button"
 aria-pressed={isSelected}
 className={[
 'relative cursor-pointer text-bamboo-100/90 transition-colors focus:outline-none focus-visible:ring-1 focus-visible:ring-bamboo-400',
 isSelected
 ? 'bg-ink-900 text-bamboo-50 before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-bamboo-400'
 : 'hover:bg-ink-900/50',
 ].join(' ')}
 >
 <td className="px-3 py-3 align-top font-medium text-bamboo-50">
 {p.hostname}
 </td>
 <td className="px-3 py-3 align-top">
 <DnsNameCell name={p.peerDnsName} />
 </td>
 <td className="px-3 py-3 align-top">
 <AddressCell
 peer={p}
 expanded={isExpanded}
 onToggle={(e) => {
 e.stopPropagation();
 setExpandedId((curr) => (curr === p.id ? null : p.id));
 }}
 />
 </td>
 <td className="px-3 py-3 align-top text-xs">
 {p.ownerEmail ? (
 <span title={p.ownerEmail} className="truncate text-bamboo-100/80">
 {p.ownerDisplayName || p.ownerEmail}
 </span>
 ) : (
 <span className="text-bamboo-200/40" title={t('ownerUnknown')}>
 {t('ownerUnknown')}
 </span>
 )}
 </td>
 <td className="px-3 py-3 align-top">
 <div className="flex flex-wrap gap-1.5">
 {p.tags.map((tag) => (
 <span
 key={tag}
 className="text-xs text-bamboo-200/70"
 >
 #{tag}
 </span>
 ))}
 </div>
 </td>
 <td className="px-3 py-3 align-top text-xs text-bamboo-200/60">{p.os}</td>
 <td className="px-3 py-3 align-top">
 <StatusBadge
 status={p.status}
 label={t(`status.${p.status}`)}
 connectionPath={p.connectionPath}
 />
 </td>
 <td className="px-3 py-3 align-top text-xs text-bamboo-200/60">
 {p.lastSeenAt ? formatRelative(p.lastSeenAt) : '—'}
 </td>
 </tr>
 );
 })}
 </tbody>
 </table>
 </div>
 );
}

// DnsNameCell renders `<label>.bamboo` with the suffix dimmed so
// the eye lands on the user-meaningful label. Empty when the peer
// hasn't been auto-slugged yet (legacy row); the cell renders an
// em-dash placeholder so columns stay aligned.
function DnsNameCell({ name }: { name: string | undefined }) {
 if (!name) {
 return <span className="text-bamboo-200/40">—</span>;
 }
 return (
 <span className="font-mono text-xs">
 <span className="text-bamboo-100">{name}</span>
 <span className="text-bamboo-200/40">.bamboo</span>
 </span>
 );
}

// AddressCell — IP + chevron disclosure that reveals the WG pubkey
// and short peer id. Same shape as the prior version; just the
// theme colors were shifted.
function AddressCell({
 peer,
 expanded,
 onToggle,
}: {
 peer: Peer;
 expanded: boolean;
 onToggle: (e: React.MouseEvent | React.KeyboardEvent) => void;
}) {
 const hasDetails = Boolean(peer.wireguardPublicKey);
 return (
 <div className="flex items-start gap-1">
 <div className="min-w-0 flex-1">
 <div className="font-mono text-xs text-bamboo-100/90">{peer.ip}</div>
 {expanded && hasDetails && (
 <dl className="mt-1.5 space-y-1 text-[10px] text-bamboo-200/60">
 <div>
 <dt className="tracking-wide text-bamboo-200/40">ID</dt>
 <dd className="font-mono text-bamboo-100/80">
 {peer.id.slice(0, 8)}
 </dd>
 </div>
 <div>
 <dt className="tracking-wide text-bamboo-200/40">Public key</dt>
 <dd className="break-all font-mono text-bamboo-100/80">
 {peer.wireguardPublicKey}
 </dd>
 </div>
 </dl>
 )}
 </div>
 {hasDetails && (
 <button
 type="button"
 onClick={onToggle}
 onKeyDown={(e) => {
 if (e.key === 'Enter' || e.key === ' ') {
 e.stopPropagation();
 }
 }}
 aria-label="Toggle address details"
 aria-expanded={expanded}
 className="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-bamboo-200/50 transition-colors hover:bg-ink-800 hover:text-bamboo-50 focus:outline-none focus-visible:ring-1 focus-visible:ring-bamboo-400"
 >
 <svg
 viewBox="0 0 24 24"
 fill="none"
 stroke="currentColor"
 strokeWidth="1.5"
 strokeLinecap="round"
 strokeLinejoin="round"
 aria-hidden
 className={`h-3.5 w-3.5 transition-transform ${expanded ? 'rotate-180' : ''}`}
 >
 <path d="m6 9 6 6 6-6" />
 </svg>
 </button>
 )}
 </div>
 );
}

function StatusBadge({
 status,
 label,
 connectionPath,
}: {
 status: Peer['status'];
 label: string;
 connectionPath?: Peer['connectionPath'];
}) {
 // Dot + label. bamboo-400 dot for online (the brand accent reads
 // as"alive" against the warm-dark surface); softer bamboo-200/40
 // for offline; hollow ink-700 ring for admin-disabled.
 const dot = {
 online: 'bg-bamboo-400',
 offline: 'bg-bamboo-200/30',
 disabled: 'border border-bamboo-200/40 bg-transparent',
 }[status];
 // Path glyph (issue #138): only shown when the peer is online —
 // a relay/direct distinction is meaningless for an offline peer.
 // 'unknown' is rendered as no glyph to keep the row clean.
 const showPath =
 status === 'online' &&
 (connectionPath === 'direct' || connectionPath === 'relay');
 return (
 <span className="inline-flex items-center gap-2 text-xs text-bamboo-100/80">
 <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot}`} />
 {label}
 {showPath && (
 <span
 className="text-bamboo-200/50"
 title={connectionPath === 'direct' ? 'direct (P2P)' : 'relay'}
 >
 {connectionPath === 'direct' ? '⚡' : '🔄'}
 </span>
 )}
 </span>
 );
}

function formatRelative(iso: string): string {
 const ms = Date.now() - new Date(iso).getTime();
 const s = Math.round(ms / 1000);
 if (s < 60) return `${s}s ago`;
 if (s < 3600) return `${Math.round(s / 60)}m ago`;
 if (s < 86_400) return `${Math.round(s / 3600)}h ago`;
 return `${Math.round(s / 86_400)}d ago`;
}
