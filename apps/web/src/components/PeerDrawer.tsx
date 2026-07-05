// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useRef, useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import {
 deletePeerAction,
 renamePeerAction,
 renamePeerDnsNameAction,
 setApprovedRoutesAction,
 setExitNodeApprovedAction,
 setNAT64EgressApprovedAction,
 setPeerStatusAction,
 setPeerTagsAction,
 setUsingExitNodeAction,
} from '@/lib/actions';
import { useDialogA11y } from '@/hooks/useDialogA11y';
import type {
 PeerBandwidthSample,
 PeerConnectionEvent,
 PeerConnectionPath,
 PeerRouteConflict,
} from '@/lib/api';
import {
 computeDeltas,
 formatBytes as formatBandwidthBytes,
 formatRate,
 sparklinePath,
 sumWindow,
} from '@/lib/bandwidth';
import type { FetchResult, Peer, PeerEvent } from '@/lib/types';

type Props = {
 // peerResult is null when nothing is selected (drawer closed) and
 // a FetchResult otherwise. The three FetchResult variants drive
 // three distinct render states inside the drawer body:
 // - kind='ok' → full peer detail (DrawerBody)
 // - kind='notFound' → peer deleted / cross-tenant / bad uuid
 // - kind='error' → controller unreachable / 5xx — distinct
 // from notFound so the user doesn't read"node was deleted"
 // when the real problem is a network outage.
 peerResult: FetchResult<Peer> | null;
 events: PeerEvent[];
 // connectionEvents drives the #138 v2 Connection-path timeline
 // section. Independent from `events` (audit log) because the data
 // source is ClickHouse, the cardinality differs, and rendering uses
 // a path-specific visual (⚡/🔄 glyph + arrow) rather than the
 // generic action labels of the audit timeline.
 connectionEvents: PeerConnectionEvent[];
 // routeConflicts surfaces approved-CIDR overlaps with other peers
 // in the same tenant (§3a route conflict detection). Side-channel
 // — empty array on any fetch failure so the drawer renders normally
 // even if the conflicts endpoint is offline. The AdvertiseSection
 // filters by CIDR to render an inline badge per affected row.
 routeConflicts: PeerRouteConflict[];
 // bandwidth is the cumulative-counter time series the §4 P2
 // BandwidthSection renders as a sparkline + last-window totals.
 // Empty array on any fetch failure (incl. CH outage / older
 // controller without the endpoint) → section renders an empty
 // state rather than taking down the drawer.
 bandwidth: PeerBandwidthSample[];
 // exitNodeOptions is the tenant's approved exit nodes — the
 // selectable targets for the "route through exit node" picker (the
 // consume side of #137). Passed from PeersView, which already holds
 // the full peer list; the picker filters out the peer itself.
 exitNodeOptions: Peer[];
 open: boolean;
 onClose: () => void;
 // onDeleted fires after a successful delete so PeersView can clear
 // the ?selected= URL query — otherwise the drawer would re-open
 // pointing at a now-404 id.
 onDeleted: () => void;
};

// PeerDrawer is the slide-in side panel that appears when a row is
// selected. `open` is driven by the URL (`?selected=<id>`) so back/
// forward and link-sharing both work; `peer` is null when the id
// resolved to 404 (deleted peer or stale link), and the drawer
// renders a not-found state in that case.
export function PeerDrawer({ peerResult, events, connectionEvents, routeConflicts, bandwidth, exitNodeOptions, open, onClose, onDeleted }: Props) {
 const peer = peerResult?.kind === 'ok' ? peerResult.value : null;
 const t = useTranslations('peers.drawer');
 const tStatus = useTranslations('peers.status');
 const panelRef = useRef<HTMLDivElement>(null);

 // Focus management + body scroll lock + ESC + Tab trap. See
 // hooks/useDialogA11y.ts for the implementation; the drawer
 // opts into trapTab=true because its role='dialog' aria-modal
 // contract promises focus stays inside.
 useDialogA11y({ open, onClose, panelRef, trapTab: true });

 return (
 <div
 aria-hidden={!open}
 className={`fixed inset-0 z-40 transition ${open ? 'pointer-events-auto' : 'pointer-events-none'}`}
 >
 <div
 className={`absolute inset-0 bg-ink-950/80 transition-opacity dark:bg-black/60 ${
 open ? 'opacity-100' : 'opacity-0'
 }`}
 onClick={onClose}
 />
 <div
 ref={panelRef}
 role="dialog"
 aria-modal="true"
 aria-labelledby="peer-drawer-title"
 className={`absolute right-0 top-0 flex h-full w-full max-w-md flex-col border-l border-ink-800 bg-ink-950 shadow-xl transition-transform ${
 open ? 'translate-x-0' : 'translate-x-full'
 }`}
 >
 <DrawerHeader peer={peer} statusLabel={peer ? tStatus(peer.status) : ''} onClose={onClose} closeLabel={t('close')} />
 <div className="flex-1 overflow-y-auto px-6 py-4">
 {renderBody(peerResult, events, connectionEvents, routeConflicts, bandwidth, exitNodeOptions, onDeleted, t)}
 </div>
 </div>
 </div>
 );
}

function DrawerHeader({
 peer,
 statusLabel,
 onClose,
 closeLabel,
}: {
 peer: Peer | null;
 statusLabel: string;
 onClose: () => void;
 closeLabel: string;
}) {
 return (
 <header className="flex items-start justify-between border-b border-ink-800 px-6 py-4">
 <div className="min-w-0">
 <h2
 id="peer-drawer-title"
 className="truncate text-lg font-semibold tracking-tight text-bamboo-50"
 >
 {peer?.hostname ?? '—'}
 </h2>
 {peer && (
 <div className="mt-1 flex items-center gap-2">
 <StatusBadge status={peer.status} label={statusLabel} />
 <span className="font-mono text-xs text-bamboo-200/60">{peer.ip}</span>
 </div>
 )}
 </div>
 <button
 type="button"
 onClick={onClose}
 aria-label={closeLabel}
 className="-mr-2 -mt-1 rounded p-2 text-bamboo-200/70 hover:bg-ink-800 hover:text-bamboo-50"
 >
 <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
 <path d="M3 3l10 10M13 3L3 13" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
 </svg>
 </button>
 </header>
 );
}

function DrawerBody({
 peer,
 events,
 connectionEvents,
 routeConflicts,
 bandwidth,
 exitNodeOptions,
 onDeleted,
}: {
 peer: Peer;
 events: PeerEvent[];
 connectionEvents: PeerConnectionEvent[];
 routeConflicts: PeerRouteConflict[];
 bandwidth: PeerBandwidthSample[];
 exitNodeOptions: Peer[];
 onDeleted: () => void;
}) {
 const t = useTranslations('peers.drawer');
 const [error, setError] = useState<string | null>(null);

 return (
 <div className="space-y-6">
 {error && (
 <div className="rounded-md border border-red-300 px-3 py-2 text-sm text-red-700 dark:border-red-900/50 dark:text-red-400">
 {t('errorPrefix')} {error}
 </div>
 )}

 <Section title={t('sections.basic')}>
 <HostnameField peer={peer} onError={setError} />
 <DnsNameField peer={peer} onError={setError} />
 <Field label={t('fields.ip')} value={peer.ip} mono />
 <Field
 label={t('fields.owner')}
 value={peer.ownerEmail ? (peer.ownerDisplayName || peer.ownerEmail) : '—'}
 />
 <Field label={t('fields.os')} value={peer.os || '—'} />
 <Field label={t('fields.clientVersion')} value={peer.clientVersion || '—'} mono />
 <TagsField peer={peer} onError={setError} />
 </Section>

 <Section title={t('sections.wireguard')}>
 <Field
 label={t('fields.publicKey')}
 value={
 peer.wireguardPublicKey ? (
 <code className="block break-all rounded border border-ink-800 px-2 py-1 font-mono text-xs text-bamboo-100">
 {peer.wireguardPublicKey}
 </code>
 ) : (
 '—'
 )
 }
 />
 </Section>

 <Section title={t('sections.connection')}>
 {peer.lastHandshakeAt ? (
 <>
 <Field
 label={t('fields.wgEndpoint')}
 value={peer.wgEndpoint ?? '—'}
 mono={Boolean(peer.wgEndpoint)}
 />
 <Field label={t('fields.rxBytes')} value={formatBytes(peer.rxBytes)} />
 <Field label={t('fields.txBytes')} value={formatBytes(peer.txBytes)} />
 <Field label={t('fields.lastHandshake')} value={formatTimestamp(peer.lastHandshakeAt)} />
 </>
 ) : (
 <p className="text-sm text-bamboo-200/60">{t('empty.noHandshake')}</p>
 )}
 </Section>

 <Section title={t('sections.endpoints')}>
 {peer.endpoints.length === 0 ? (
 <p className="text-sm text-bamboo-200/60">{t('empty.endpoints')}</p>
 ) : (
 <ul className="space-y-1">
 {peer.endpoints.map((ep) => (
 <li
 key={ep}
 className="rounded border border-ink-800 px-2 py-1 font-mono text-xs text-bamboo-100"
 >
 {ep}
 </li>
 ))}
 </ul>
 )}
 </Section>

 <Section title={t('sections.timestamps')}>
 <Field label={t('fields.createdAt')} value={formatTimestamp(peer.createdAt)} />
 <Field
 label={t('fields.lastSeen')}
 value={peer.lastSeenAt ? formatTimestamp(peer.lastSeenAt) : '—'}
 />
 </Section>

 <AdvertiseSection peer={peer} routeConflicts={routeConflicts} onError={setError} />

 <UseExitNodeSection peer={peer} options={exitNodeOptions} onError={setError} />

 <Section title={t('sections.actions')}>
 <DisableToggle peer={peer} onError={setError} />
 <DeleteButton peer={peer} onError={setError} onDeleted={onDeleted} />
 </Section>

 <Section title={t('sections.bandwidth')}>
 <BandwidthSection samples={bandwidth} />
 </Section>

 <Section title={t('sections.connectionTimeline')}>
 <ConnectionTimeline events={connectionEvents} />
 </Section>

 <Section title={t('sections.timeline')}>
 <Timeline events={events} />
 </Section>
 </div>
 );
}

// HostnameField wraps the basic-info hostname row with click-to-edit
// behavior: shows the value with a small"edit" affordance until
// clicked, then turns into a text input with save / cancel buttons.
// Enter saves, Esc cancels.
function HostnameField({ peer, onError }: { peer: Peer; onError: (msg: string | null) => void }) {
 const t = useTranslations('peers.drawer');
 const [editing, setEditing] = useState(false);
 const [draft, setDraft] = useState(peer.hostname);
 const [pending, startTransition] = useTransition();

 function save() {
 const next = draft.trim();
 if (next === '' || next === peer.hostname) {
 setEditing(false);
 setDraft(peer.hostname);
 return;
 }
 startTransition(async () => {
 const res = await renamePeerAction(peer.id, next);
 if (res.ok) {
 setEditing(false);
 onError(null);
 } else {
 onError(res.error);
 }
 });
 }

 return (
 <div className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
 <dt className="text-bamboo-200/60">{t('fields.hostname')}</dt>
 <dd className="min-w-0 break-words text-bamboo-50">
 {editing ? (
 <div className="flex gap-2">
 <input
 autoFocus
 type="text"
 value={draft}
 disabled={pending}
 onChange={(e) => setDraft(e.target.value)}
 onKeyDown={(e) => {
 if (e.key === 'Enter') save();
 if (e.key === 'Escape') {
 setEditing(false);
 setDraft(peer.hostname);
 }
 }}
 className="flex-1 rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1 text-sm text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
 />
 <button
 type="button"
 onClick={save}
 disabled={pending}
 className="rounded border border-bamboo-50 bg-bamboo-50 px-2 text-xs font-medium text-ink-950 transition-colors hover:bg-ink-800 disabled:opacity-50"
 >
 {t('inline.save')}
 </button>
 <button
 type="button"
 onClick={() => {
 setEditing(false);
 setDraft(peer.hostname);
 }}
 disabled={pending}
 className="rounded border border-bamboo-200/30 px-2 text-xs text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50"
 >
 {t('inline.cancel')}
 </button>
 </div>
 ) : (
 <button
 type="button"
 onClick={() => setEditing(true)}
 className="group flex items-center gap-2 text-left"
 title={t('inline.editHostname')}
 >
 <span>{peer.hostname}</span>
 <span className="text-xs text-bamboo-200/40 opacity-0 group-hover:opacity-100">
 ✎
 </span>
 </button>
 )}
 </dd>
 </div>
 );
}

// DnsNameField mirrors HostnameField's click-to-edit pattern for
// the MagicDNS label. Server-side the controller's PATCH handler
// validates strictly: lowercase, `[a-z0-9-]`, no leading/trailing
// dash, ≤63 chars; 409 on collision within the tenant. We let the
// server return the error and surface it via onError rather than
// pre-validate here — keeps the rule in one place and ensures the
// UI matches whatever the slug logic enforces today.
//
// Display form: `<label>.bamboo` with the suffix dimmed (same
// pattern as the PeerTable DNS-name cell). Editing replaces the
// display with a plain text input pre-populated with the bare
// label.
function DnsNameField({ peer, onError }: { peer: Peer; onError: (msg: string | null) => void }) {
 const t = useTranslations('peers.drawer');
 const [editing, setEditing] = useState(false);
 const [draft, setDraft] = useState(peer.peerDnsName ?? '');
 const [pending, startTransition] = useTransition();

 function save() {
 const next = draft.trim();
 if (next === (peer.peerDnsName ?? '')) {
 setEditing(false);
 return;
 }
 startTransition(async () => {
 const res = await renamePeerDnsNameAction(peer.id, next);
 if (res.ok) {
 setEditing(false);
 onError(null);
 } else {
 onError(res.error);
 }
 });
 }

 return (
 <div className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
 <dt className="text-bamboo-200/60">{t('fields.dnsName')}</dt>
 <dd className="min-w-0 break-words text-bamboo-50">
 {editing ? (
 <div className="flex gap-2">
 <input
 autoFocus
 type="text"
 value={draft}
 disabled={pending}
 onChange={(e) => setDraft(e.target.value)}
 onKeyDown={(e) => {
 if (e.key === 'Enter') save();
 if (e.key === 'Escape') {
 setEditing(false);
 setDraft(peer.peerDnsName ?? '');
 }
 }}
 placeholder={t('inline.dnsNamePlaceholder')}
 className="flex-1 rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1 font-mono text-sm text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
 />
 <button
 type="button"
 onClick={save}
 disabled={pending}
 className="rounded border border-bamboo-50 bg-bamboo-50 px-2 text-xs font-medium text-ink-950 transition-colors hover:bg-ink-800 disabled:opacity-50"
 >
 {t('inline.save')}
 </button>
 <button
 type="button"
 onClick={() => {
 setEditing(false);
 setDraft(peer.peerDnsName ?? '');
 }}
 disabled={pending}
 className="rounded border border-bamboo-200/30 px-2 text-xs text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50"
 >
 {t('inline.cancel')}
 </button>
 </div>
 ) : (
 <button
 type="button"
 onClick={() => setEditing(true)}
 className="group flex items-center gap-2 text-left"
 title={t('inline.editDnsName')}
 >
 {peer.peerDnsName ? (
 <span className="font-mono text-sm">
 <span>{peer.peerDnsName}</span>
 <span className="text-bamboo-200/40">.bamboo</span>
 </span>
 ) : (
 <span className="font-mono text-sm text-bamboo-200/40">
 {t('inline.dnsNameUnset')}
 </span>
 )}
 <span className="text-xs text-bamboo-200/40 opacity-0 group-hover:opacity-100">
 ✎
 </span>
 </button>
 )}
 </dd>
 </div>
 );
}

// TagsField turns the read-only tags row into an editable comma-
// separated input. Blur commits the change; we accept the server's
// canonical form (sorted, trimmed) implicitly via the next re-render
// because the Server Action revalidates the page.
function TagsField({ peer, onError }: { peer: Peer; onError: (msg: string | null) => void }) {
 const t = useTranslations('peers.drawer');
 const [editing, setEditing] = useState(false);
 const [draft, setDraft] = useState(peer.tags.join(', '));
 const [pending, startTransition] = useTransition();

 function commit() {
 // Defer the action; toggling editing first avoids a stuck state
 // when the user blurs the input by clicking elsewhere.
 setEditing(false);
 const next = draft
 .split(',')
 .map((s) => s.trim())
 .filter((s) => s !== '');
 const same = next.length === peer.tags.length && next.every((v, i) => v === peer.tags[i]);
 if (same) return;
 startTransition(async () => {
 const res = await setPeerTagsAction(peer.id, next);
 if (res.ok) {
 onError(null);
 } else {
 onError(res.error);
 }
 });
 }

 return (
 <div className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
 <dt className="text-bamboo-200/60">{t('fields.tags')}</dt>
 <dd className="min-w-0 break-words text-bamboo-50">
 {editing ? (
 <input
 autoFocus
 type="text"
 value={draft}
 disabled={pending}
 placeholder={t('inline.tagsPlaceholder')}
 onChange={(e) => setDraft(e.target.value)}
 onBlur={commit}
 onKeyDown={(e) => {
 if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
 if (e.key === 'Escape') {
 setEditing(false);
 setDraft(peer.tags.join(', '));
 }
 }}
 className="w-full rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1 text-sm text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
 />
 ) : (
 <button
 type="button"
 onClick={() => {
 setDraft(peer.tags.join(', '));
 setEditing(true);
 }}
 className="group flex w-full flex-wrap items-center gap-1 text-left"
 title={t('inline.editTags')}
 >
 {peer.tags.length === 0 ? (
 <span className="text-bamboo-200/60">{t('empty.tags')}</span>
 ) : (
 peer.tags.map((tag) => (
 <span
 key={tag}
 className="rounded border border-ink-800 px-2 py-0.5 text-xs text-bamboo-200/70"
 >
 {tag}
 </span>
 ))
 )}
 <span className="text-xs text-bamboo-200/40 opacity-0 group-hover:opacity-100">✎</span>
 </button>
 )}
 </dd>
 </div>
 );
}

// DisableToggle flips a peer between 'disabled' and 'online'. The
// online/offline distinction reverts to reporter control on the
// next tick — we don't try to remember the last reporter-derived
// status before disabling. Re-enabling sets 'online' optimistically
// and trusts the reporter to correct it.
function DisableToggle({ peer, onError }: { peer: Peer; onError: (msg: string | null) => void }) {
 const t = useTranslations('peers.drawer');
 const [pending, startTransition] = useTransition();
 const isDisabled = peer.status === 'disabled';
 const label = isDisabled ? t('actions.enable') : t('actions.disable');
 const nextStatus = isDisabled ? 'online' : 'disabled';

 return (
 <button
 type="button"
 disabled={pending}
 onClick={() => {
 startTransition(async () => {
 const res = await setPeerStatusAction(peer.id, nextStatus);
 if (res.ok) onError(null);
 else onError(res.error);
 });
 }}
 className="w-full rounded-md border border-bamboo-200/30 px-3 py-1.5 text-left text-sm text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 disabled:opacity-50"
 >
 {pending ? t('inline.working') : label}
 </button>
 );
}

// AdvertiseSection surfaces what the peer has asked to advertise
// (subnet routes via #136, exit-node role via #137) and lets the
// admin approve a subset row-by-row. Hidden when the peer has
// advertised nothing AND isn't exit-node-capable — keeps the
// common 90% case visually quiet. Reads fields off `peer` and
// posts changes via setApprovedRoutesAction /
// setExitNodeApprovedAction; revalidation refreshes the canonical
// state.
function AdvertiseSection({
 peer,
 routeConflicts,
 onError,
}: {
 peer: Peer;
 routeConflicts: PeerRouteConflict[];
 onError: (msg: string | null) => void;
}) {
 const t = useTranslations('peers.drawer');
 if (peer.advertisedRoutes.length === 0 && !peer.exitNodeCapable && !peer.nat64EgressCapable) {
 return null;
 }
 return (
 <Section title={t('sections.advertise')}>
 {peer.advertisedRoutes.length > 0 && (
 <div className="space-y-2">
 <p className="text-xs text-bamboo-200/60">{t('advertise.routesHint')}</p>
 {peer.advertisedRoutes.map((cidr) => (
 <RouteApprovalRow
 key={cidr}
 peer={peer}
 cidr={cidr}
 conflicts={routeConflicts.filter((c) => c.cidr === cidr)}
 onError={onError}
 />
 ))}
 </div>
 )}
 {peer.exitNodeCapable && (
 <ExitNodeApprovalRow peer={peer} onError={onError} />
 )}
 {peer.nat64EgressCapable && (
 <NAT64EgressApprovalRow peer={peer} onError={onError} />
 )}
 </Section>
 );
}

// RouteApprovalRow is one (CIDR, approved?) row. Clicking the
// checkbox computes the new full approved set (current +/- this
// CIDR) and posts it. The race window where two quick clicks both
// read the pre-click approvedRoutes is acceptable: revalidatePath
// refreshes the canonical state from the controller, and the
// consequence of losing a click is one missed approval, not a
// security issue.
//
// When the row's CIDR collides with another peer's approved CIDR,
// the per-conflict badges below the checkbox name the other peer
// + the relationship (duplicate / contains / contained_by). The
// detector runs against the current approved set, so the warning
// disappears the instant the operator un-approves the row that
// caused the overlap.
function RouteApprovalRow({
 peer,
 cidr,
 conflicts,
 onError,
}: {
 peer: Peer;
 cidr: string;
 conflicts: PeerRouteConflict[];
 onError: (msg: string | null) => void;
}) {
 const t = useTranslations('peers.drawer');
 const [pending, startTransition] = useTransition();
 const approved = peer.approvedRoutes.includes(cidr);
 const hasConflicts = approved && conflicts.length > 0;
 return (
 <div className={`rounded-md border ${hasConflicts ? 'border-amber-500/40' : 'border-bamboo-200/20'} px-3 py-1.5 text-sm text-bamboo-100`}>
 <label className="flex items-center gap-2">
 <input
 type="checkbox"
 checked={approved}
 disabled={pending}
 onChange={() => {
 startTransition(async () => {
 const next = approved
 ? peer.approvedRoutes.filter((r) => r !== cidr)
 : [...peer.approvedRoutes, cidr];
 const res = await setApprovedRoutesAction(peer.id, next);
 if (res.ok) onError(null);
 else onError(res.error);
 });
 }}
 />
 <code className="flex-1 font-mono text-xs">{cidr}</code>
 {hasConflicts && (
 <span
 aria-hidden="true"
 title={t('routeConflicts.summary', { count: conflicts.length })}
 className="text-xs text-amber-300"
 >
 ⚠ {conflicts.length}
 </span>
 )}
 {pending && <span className="text-xs text-bamboo-200/60">{t('inline.working')}</span>}
 </label>
 {hasConflicts && (
 <ul className="mt-1.5 space-y-0.5 pl-7 text-[11px] text-amber-200/80">
 {conflicts.map((c) => (
 <li key={`${c.otherPeerId}:${c.otherCidr}:${c.kind}`}>
 {t(`routeConflicts.kind.${c.kind}` as never, {
 host: c.otherHostname,
 cidr: c.otherCidr,
 })}
 </li>
 ))}
 </ul>
 )}
 </div>
 );
}

// ExitNodeApprovalRow is the single-toggle equivalent of
// RouteApprovalRow. Revoking (false) is always allowed regardless
// of capable state; approving (true) requires the peer to remain
// exit_node_capable, which the parent component already guards
// (this row is only rendered when capable is true).
function ExitNodeApprovalRow({
 peer,
 onError,
}: {
 peer: Peer;
 onError: (msg: string | null) => void;
}) {
 const t = useTranslations('peers.drawer');
 const [pending, startTransition] = useTransition();
 return (
 <label className="flex items-center gap-2 rounded-md border border-bamboo-200/20 px-3 py-1.5 text-sm text-bamboo-100">
 <input
 type="checkbox"
 checked={peer.exitNodeApproved}
 disabled={pending}
 onChange={() => {
 startTransition(async () => {
 const res = await setExitNodeApprovedAction(peer.id, !peer.exitNodeApproved);
 if (res.ok) onError(null);
 else onError(res.error);
 });
 }}
 />
 <span className="flex-1">{t('advertise.exitNode')}</span>
 {pending && <span className="text-xs text-bamboo-200/60">{t('inline.working')}</span>}
 </label>
 );
}

// UseExitNodeSection is the CONSUME side of exit nodes (#137): it lets
// an admin route THIS peer's default traffic through an approved exit
// node. Distinct from ExitNodeApprovalRow (which marks a peer AS an exit
// node). Rendered only when there's an approved exit node to pick
// (excluding the peer itself) OR the peer already uses one — so it
// doesn't clutter every drawer in tenants with no exit nodes.
function UseExitNodeSection({
 peer,
 options,
 onError,
}: {
 peer: Peer;
 options: Peer[];
 onError: (msg: string | null) => void;
}) {
 const t = useTranslations('peers.drawer');
 const [pending, startTransition] = useTransition();
 const selectable = options.filter((o) => o.id !== peer.id);
 const current = peer.usingExitNodePeerId ?? '';
 if (selectable.length === 0 && current === '') return null;

 function apply(next: string) {
 startTransition(async () => {
 const res = await setUsingExitNodeAction(peer.id, next === '' ? null : next);
 if (res.ok) onError(null);
 else onError(res.error);
 });
 }

 return (
 <Section title={t('sections.useExitNode')}>
 <label className="flex items-center gap-2 text-sm text-bamboo-100">
 <span className="flex-1">{t('useExitNode.label')}</span>
 <select
 value={current}
 disabled={pending}
 onChange={(e) => apply(e.target.value)}
 className="rounded-md border border-bamboo-200/30 bg-ink-950 px-2 py-1 text-sm text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300 disabled:opacity-50"
 >
 <option value="">{t('useExitNode.none')}</option>
 {selectable.map((o) => (
 <option key={o.id} value={o.id}>
 {o.hostname || o.id}
 </option>
 ))}
 </select>
 </label>
 <p className="text-xs text-bamboo-200/60">{t('useExitNode.hint')}</p>
 {pending && <span className="text-xs text-bamboo-200/60">{t('inline.working')}</span>}
 </Section>
 );
}

// NAT64EgressApprovalRow is the single-toggle equivalent of
// RouteApprovalRow. Revoking (false) is always allowed regardless
// of capable state; approving (true) requires the peer to remain
// nat64_egress_capable, which the parent component already guards
// (this row is only rendered when capable is true).
function NAT64EgressApprovalRow({
 peer,
 onError,
}: {
 peer: Peer;
 onError: (msg: string | null) => void;
}) {
 const t = useTranslations('peers.drawer');
 const [pending, startTransition] = useTransition();
 return (
 <label className="flex items-center gap-2 rounded-md border border-bamboo-200/20 px-3 py-1.5 text-sm text-bamboo-100">
 <input
 type="checkbox"
 checked={peer.nat64EgressApproved}
 disabled={pending}
 onChange={() => {
 startTransition(async () => {
 const res = await setNAT64EgressApprovedAction(peer.id, !peer.nat64EgressApproved);
 if (res.ok) onError(null);
 else onError(res.error);
 });
 }}
 />
 <span className="flex-1">{t('advertise.nat64Egress')}</span>
 {pending && <span className="text-xs text-bamboo-200/60">{t('inline.working')}</span>}
 </label>
 );
}

// DeleteButton uses a two-stage confirm pattern instead of a modal:
// first click reveals"確定 / 取消" buttons inline; second click on
// confirm fires the action. Avoids importing a modal lib for one
// destructive verb.
function DeleteButton({
 peer,
 onError,
 onDeleted,
}: {
 peer: Peer;
 onError: (msg: string | null) => void;
 onDeleted: () => void;
}) {
 const t = useTranslations('peers.drawer');
 const [confirming, setConfirming] = useState(false);
 const [pending, startTransition] = useTransition();

 if (confirming) {
 return (
 <div className="space-y-1">
 <p className="text-sm text-red-700 dark:text-red-300">{t('actions.confirmDelete')}</p>
 <div className="flex gap-2">
 <button
 type="button"
 disabled={pending}
 onClick={() => {
 startTransition(async () => {
 const res = await deletePeerAction(peer.id);
 if (res.ok) {
 onError(null);
 onDeleted();
 } else {
 onError(res.error);
 setConfirming(false);
 }
 });
 }}
 className="flex-1 rounded-md border border-red-300 px-3 py-1.5 text-sm font-medium text-red-700 transition-colors hover:border-red-400 hover:bg-red-50 disabled:opacity-50 dark:border-red-900/50 dark:text-red-400 dark:hover:bg-red-950/40"
 >
 {pending ? t('inline.working') : t('actions.confirmYes')}
 </button>
 <button
 type="button"
 disabled={pending}
 onClick={() => setConfirming(false)}
 className="flex-1 rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 disabled:opacity-50"
 >
 {t('actions.confirmCancel')}
 </button>
 </div>
 </div>
 );
 }

 return (
 <button
 type="button"
 onClick={() => setConfirming(true)}
 className="w-full rounded-md border border-bamboo-200/30 px-3 py-1.5 text-left text-sm text-bamboo-100 transition-colors hover:border-red-300 hover:text-red-700 dark:hover:border-red-900/50 dark:hover:text-red-400"
 >
 {t('actions.delete')}
 </button>
 );
}

// BandwidthSection renders the §4 P2 per-peer bandwidth view: a
// sparkline of recent throughput stacked over the cumulative totals
// for the displayed window. Cumulative wg counters arrive at
// heartbeat cadence (~30s); we compute deltas client-side via
// lib/bandwidth.ts so the controller stays stateless.
//
// We render TWO sparklines stacked (tx on top, rx below) instead of
// overlaid because overlaid lines on a 28px-tall sparkline cross
// each other constantly and become unreadable. Stacked also keeps
// the labels honest — admins can see "this peer is upload-heavy"
// without color-coding intuition.
//
// Empty state (< 2 samples) explains why the section is blank: the
// CLI feature ships in #188, controllers without it produce no
// rows, and a never-talked-to peer has no samples either. We tell
// the user instead of rendering a misleading empty axis.
function BandwidthSection({ samples }: { samples: PeerBandwidthSample[] }) {
 const t = useTranslations('peers.drawer');
 const deltas = computeDeltas(samples);
 if (deltas.length === 0) {
  return <p className="text-sm text-bamboo-200/60">{t('empty.bandwidth')}</p>;
 }
 const totals = sumWindow(deltas);
 const sentSeries = deltas.map((d) => d.sentBytesPerSec);
 const recvSeries = deltas.map((d) => d.receivedBytesPerSec);
 const peakSent = Math.max(...sentSeries);
 const peakRecv = Math.max(...recvSeries);
 const window = formatRelativeWindow(deltas[0].startedAt, deltas[deltas.length - 1].endedAt);
 return (
  <div className="space-y-3">
   <p className="text-xs text-bamboo-200/60">{t('bandwidth.window', { window })}</p>
   <SparklineRow
    label={t('fields.txBytes')}
    series={sentSeries}
    total={totals.sent}
    peak={peakSent}
   />
   <SparklineRow
    label={t('fields.rxBytes')}
    series={recvSeries}
    total={totals.received}
    peak={peakRecv}
   />
  </div>
 );
}

// SparklineRow is one row inside BandwidthSection: label + tiny SVG
// path + total-in-window + peak-rate. The 240×28 viewBox is sized
// to fit the drawer column without horizontal scrolling at any zoom
// down to ~min-tablet width. stroke-width 1.25 reads well against
// the warm-dark ink-900 surface without dominating the text labels.
function SparklineRow({
 label,
 series,
 total,
 peak,
}: {
 label: string;
 series: number[];
 total: number;
 peak: number;
}) {
 const t = useTranslations('peers.drawer');
 const path = sparklinePath(series, 240, 28);
 return (
  <div className="space-y-1">
   <div className="flex items-baseline justify-between gap-3 text-xs">
    <span className="text-bamboo-200/70">{label}</span>
    <span className="text-bamboo-100">
     {formatBandwidthBytes(total)}
     <span className="ml-2 text-bamboo-200/60">{t('bandwidth.peak', { rate: formatRate(peak) })}</span>
    </span>
   </div>
   <svg
    role="img"
    aria-label={t('bandwidth.sparklineAria', { label, total: formatBandwidthBytes(total) })}
    viewBox="0 0 240 28"
    preserveAspectRatio="none"
    className="block h-7 w-full"
   >
    {path && (
     <path
      d={path}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.25"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="text-bamboo-300"
     />
    )}
   </svg>
  </div>
 );
}

// formatRelativeWindow renders the chart's covered time range as a
// terse "last Xh" / "last Xd" string. Keeps the section header
// honest about how much history backs the visible bars without
// asking the operator to do mental math from two timestamps.
function formatRelativeWindow(start: Date, end: Date): string {
 const seconds = Math.max(0, (end.getTime() - start.getTime()) / 1000);
 if (seconds < 60) return `${Math.round(seconds)}s`;
 const minutes = seconds / 60;
 if (minutes < 60) return `${Math.round(minutes)}m`;
 const hours = minutes / 60;
 if (hours < 48) return `${Math.round(hours)}h`;
 return `${Math.round(hours / 24)}d`;
}

// ConnectionTimeline renders the per-peer path-transition log (issue
// #138 v2). Newest-first list of "newPath ← prevPath at relative
// time" entries, mirroring the ⚡/🔄 glyph the PeerTable status
// column already uses so admins build one mental model across the
// two surfaces.
//
// Empty state is the common case: a steady direct connection produces
// zero rows. We render an explanatory line so the section doesn't
// look broken — distinct from"no activity yet" of the audit timeline
// because"no transitions" is a healthy state, not a missing-data
// state.
function ConnectionTimeline({ events }: { events: PeerConnectionEvent[] }) {
 const t = useTranslations('peers.drawer');
 if (events.length === 0) {
 return <p className="text-sm text-bamboo-200/60">{t('empty.connectionTimeline')}</p>;
 }
 return (
 <ol className="space-y-2" aria-label={t('sections.connectionTimeline')}>
 {events.map((e) => (
 <li
 key={e.id}
 className="flex items-baseline justify-between gap-3 rounded-md border border-ink-800 px-3 py-2 text-sm"
 >
 <span className="flex items-baseline gap-2 text-bamboo-50">
 <PathGlyph path={e.path} />
 <span>{t(`connectionTimeline.path.${pathKey(e.path)}` as never)}</span>
 {e.prevPath && (
 <>
 <span className="text-bamboo-200/40" aria-hidden="true">←</span>
 <PathGlyph path={e.prevPath} muted />
 <span className="text-bamboo-200/60">
 {t(`connectionTimeline.path.${pathKey(e.prevPath)}` as never)}
 </span>
 </>
 )}
 {e.rttMs ? (
 <span className="text-xs text-bamboo-200/50">{e.rttMs}ms</span>
 ) : null}
 </span>
 <span className="shrink-0 text-xs text-bamboo-200/60">
 {formatRelative(e.occurredAt)}
 </span>
 </li>
 ))}
 </ol>
 );
}

// PathGlyph is the inline ⚡/🔄/? icon shared between this timeline
// section and the table column. Mirrors PeerTable.tsx so admins
// build one mental model for the two surfaces.
function PathGlyph({ path, muted = false }: { path: PeerConnectionPath; muted?: boolean }) {
 const cls = muted ? 'text-bamboo-200/40' : 'text-bamboo-200/80';
 const glyph = path === 'direct' ? '⚡' : path === 'relay' ? '🔄' : '?';
 return (
 <span aria-hidden="true" className={cls}>
 {glyph}
 </span>
 );
}

// pathKey passes the controller's path through the i18n-key gate.
// The wire type is already narrowed to PeerConnectionPath, but any
// future widening at the type layer falls back to "unknown" here so
// a new server-side value (e.g. quic-stream) doesn't crash the
// timeline before the locales catch up.
function pathKey(path: PeerConnectionPath): PeerConnectionPath {
 if (path === 'direct' || path === 'relay') return path;
 return 'unknown';
}

// Timeline renders the per-peer audit log newest-first. Each entry
// shows action label + actor + relative time, with an inline diff
// summary tuned to the action's diff shape:
// - peer.update: per-field"key: from → to" lines (the only diff
// shape the controller actively renames toward field-level)
// - peer.register / peer.delete: small key: value list of the
// attribute snapshot the controller wrote
// Unknown actions or malformed diffs fall back to pretty JSON.
function Timeline({ events }: { events: PeerEvent[] }) {
 const t = useTranslations('peers.drawer');
 if (events.length === 0) {
 return <p className="text-sm text-bamboo-200/60">{t('empty.timeline')}</p>;
 }
 return (
 <ol className="space-y-3">
 {events.map((e) => (
 <li
 key={e.id}
 className="rounded-md border border-ink-800 px-3 py-2"
 >
 <div className="flex items-baseline justify-between gap-2 text-sm">
 <span className="font-medium text-bamboo-50">
 {actionLabel(t, e.action)}
 </span>
 <span className="shrink-0 text-xs text-bamboo-200/60">
 {formatRelative(e.occurredAt)}
 </span>
 </div>
 <div className="mt-0.5 text-xs text-bamboo-200/60">
 {actorLabel(t, e)}
 </div>
 {e.diff && <DiffRender action={e.action} diff={e.diff} />}
 </li>
 ))}
 </ol>
 );
}

// Whitelists of audit action / actor type strings that have a
// matching messages-JSON entry. next-intl 3.x throws on missing
// keys by default (no onError handler is configured in this app),
// so calling t() with an unknown suffix would crash the Timeline
// for that event. Keeping these in sync with messages/{en,zh-TW}.json
// is intentional — when the controller ships a new audit kind, the
// Web UI falls back to the raw event name until the locale catches
// up.
const KNOWN_TIMELINE_ACTIONS = new Set(['peer.register', 'peer.update', 'peer.delete']);

const KNOWN_ACTOR_TYPES = new Set<PeerEvent['actorType']>(['user', 'system', 'api']);

function actionLabel(t: ReturnType<typeof useTranslations>, action: string): string {
 if (KNOWN_TIMELINE_ACTIONS.has(action)) {
 // next-intl treats"." as namespace nesting, so we can't key the
 // JSON with `peer.register` literally. The audit log on the wire
 // still uses dotted form; the i18n JSON uses `peer_register` and
 // we translate at lookup time.
 return t(`timeline.action.${action.replaceAll('.', '_')}` as never);
 }
 return action;
}

function actorLabel(t: ReturnType<typeof useTranslations>, e: PeerEvent): string {
 if (e.actorType === 'user' && e.actorEmail) {
 return e.actorEmail;
 }
 if (KNOWN_ACTOR_TYPES.has(e.actorType)) {
 return t(`timeline.actor.${e.actorType}` as never);
 }
 return e.actorType;
}

function DiffRender({ action, diff }: { action: string; diff: Record<string, unknown> }) {
 // peer.update is the structured-diff case: every value is
 // { from, to }. Render each as a tight line.
 if (action === 'peer.update') {
 return (
 <ul className="mt-2 space-y-1 text-xs">
 {Object.entries(diff).map(([key, val]) => {
 const ft = val as { from?: unknown; to?: unknown } | undefined;
 if (!ft || !('from' in ft) || !('to' in ft)) return null;
 return (
 <li key={key} className="flex flex-wrap items-baseline gap-1">
 <span className="font-medium text-bamboo-200/70">{key}:</span>
 <code className="rounded border border-ink-800 px-1 font-mono text-bamboo-200/40 line-through decoration-bamboo-200/30">
 {formatDiffValue(ft.from)}
 </code>
 <span className="text-bamboo-200/40">→</span>
 <code className="rounded border border-bamboo-300 px-1 font-mono text-bamboo-700 dark:border-bamboo-700 dark:text-bamboo-300">
 {formatDiffValue(ft.to)}
 </code>
 </li>
 );
 })}
 </ul>
 );
 }
 // peer.register / peer.delete: shallow key: value snapshot.
 return (
 <ul className="mt-2 space-y-0.5 text-xs">
 {Object.entries(diff).map(([key, val]) => (
 <li key={key} className="flex flex-wrap items-baseline gap-1">
 <span className="text-bamboo-200/60">{key}:</span>
 <code className="rounded border border-ink-800 px-1 font-mono text-bamboo-100">
 {formatDiffValue(val)}
 </code>
 </li>
 ))}
 </ul>
 );
}

function formatDiffValue(v: unknown): string {
 if (v === null || v === undefined) return '—';
 if (typeof v === 'string') return v === '' ? '∅' : v;
 if (Array.isArray(v)) return v.length === 0 ? '[]' : v.join(', ');
 return JSON.stringify(v);
}

// formatRelative produces"3m ago" style strings. Granularity is
// deliberately coarse — timelines are scanned, not measured.
function formatRelative(iso: string): string {
 const ms = Date.now() - new Date(iso).getTime();
 if (!Number.isFinite(ms) || ms < 0) return '—';
 const s = Math.round(ms / 1000);
 if (s < 60) return `${s}s`;
 if (s < 3600) return `${Math.round(s / 60)}m`;
 if (s < 86_400) return `${Math.round(s / 3600)}h`;
 return `${Math.round(s / 86_400)}d`;
}

// renderBody picks the right body variant for the four states the
// drawer can be in: nothing selected (renders nothing, drawer is
// slid off-screen anyway), peer loaded, peer not found, fetch
// errored. Split out so the JSX in the main component stays flat.
function renderBody(
 peerResult: FetchResult<Peer> | null,
 events: PeerEvent[],
 connectionEvents: PeerConnectionEvent[],
 routeConflicts: PeerRouteConflict[],
 bandwidth: PeerBandwidthSample[],
 exitNodeOptions: Peer[],
 onDeleted: () => void,
 t: ReturnType<typeof useTranslations>,
) {
 if (peerResult === null) return null;
 switch (peerResult.kind) {
 case 'ok':
 // key forces the body to remount on peer-change so the
 // inline edit / confirm state resets between selections.
 return (
 <DrawerBody
 key={peerResult.value.id}
 peer={peerResult.value}
 events={events}
 connectionEvents={connectionEvents}
 routeConflicts={routeConflicts}
 bandwidth={bandwidth}
 exitNodeOptions={exitNodeOptions}
 onDeleted={onDeleted}
 />
 );
 case 'notFound':
 return <CenteredMessage tone="muted" message={t('notFound')} />;
 case 'error':
 return (
 <CenteredMessage
 tone="error"
 message={t('fetchError')}
 detail={peerResult.message}
 />
 );
 }
}

// CenteredMessage is the shared layout for the drawer's two fallback
// states (not-found, fetch-error). Tone toggles between the muted
// gray of"expected miss" and the red of"something is wrong".
function CenteredMessage({
 tone,
 message,
 detail,
}: {
 tone: 'muted' | 'error';
 message: string;
 detail?: string;
}) {
 const colorMain =
 tone === 'error' ? 'text-red-700 dark:text-red-300' : 'text-bamboo-200/60';
 const colorDetail =
 tone === 'error' ? 'text-red-600 dark:text-red-400' : 'text-bamboo-200/40';
 return (
 <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
 <p className={`text-sm ${colorMain}`}>{message}</p>
 {detail && <p className={`max-w-xs break-words text-xs ${colorDetail}`}>{detail}</p>}
 </div>
 );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
 return (
 <section className="space-y-2">
 <h3 className="text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
 {title}
 </h3>
 <div className="space-y-2">{children}</div>
 </section>
 );
}

function Field({
 label,
 value,
 mono = false,
}: {
 label: string;
 value: React.ReactNode;
 mono?: boolean;
}) {
 return (
 <div className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
 <dt className="text-bamboo-200/60">{label}</dt>
 <dd className={`min-w-0 break-words text-bamboo-50 ${mono ? 'font-mono text-xs' : ''}`}>
 {value}
 </dd>
 </div>
 );
}

function StatusBadge({ status, label }: { status: Peer['status']; label: string }) {
 // Same dot+label pattern as PeerTable — keep both surfaces visually
 // identical. bamboo-500 solid dot for online (alive), zinc-400 solid
 // for offline, hollow zinc dot for disabled.
 const dot = {
 online: 'bg-bamboo-500',
 offline: 'bg-bamboo-200/30',
 disabled: 'border border-bamboo-200/40 bg-transparent',
 }[status];
 return (
 <span className="inline-flex items-center gap-2 text-xs text-bamboo-100">
 <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot}`} />
 {label}
 </span>
 );
}

function formatTimestamp(iso: string): string {
 const d = new Date(iso);
 if (Number.isNaN(d.getTime())) return '—';
 return d.toISOString().replace('T', ' ').replace(/\..+$/, ' UTC');
}

// formatBytes turns raw byte counts into a binary-prefixed string
// (KiB / MiB / GiB). We use binary because wg's counters are exact
// integers; rendering them with SI prefixes would imply rounding.
function formatBytes(n: number): string {
 if (!Number.isFinite(n) || n < 0) return '—';
 if (n < 1024) return `${n} B`;
 const units = ['KiB', 'MiB', 'GiB', 'TiB'];
 let value = n / 1024;
 let unit = units[0];
 for (let i = 1; i < units.length && value >= 1024; i += 1) {
 value /= 1024;
 unit = units[i];
 }
 // 1 decimal place is enough to distinguish 1.2 from 1.3 MiB without
 // implying spurious precision.
 return `${value.toFixed(1)} ${unit}`;
}
