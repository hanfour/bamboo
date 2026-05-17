// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import type { ActivityEvent } from '@/lib/types';

// ActivityFeed renders the tenant-wide audit log on the dashboard.
// Mirrors the per-peer Timeline (PeerDrawer.tsx) layout — newest-
// first cards with action label + actor + relative time + diff —
// but is its own component because the keys live in a different
// i18n namespace (dashboard.activity vs peers.drawer.timeline) and
// the resource line (peer / policy / etc.) is unique to the
// tenant-wide view.
export function ActivityFeed({ events }: { events: ActivityEvent[] }) {
 const t = useTranslations('dashboard.activity');
 if (events.length === 0) {
 return <p className="text-sm text-bamboo-200/60">{t('empty')}</p>;
 }
 return (
 <ol className="divide-y divide-ink-800/70">
 {events.map((e) => (
 <li key={e.id} className="py-3 first:pt-0">
 <div className="flex items-baseline justify-between gap-2 text-sm">
 <span className="font-medium text-bamboo-50">
 {actionLabel(t, e.action)}
 </span>
 <span className="shrink-0 text-xs text-bamboo-200/50">
 {formatRelative(e.occurredAt)}
 </span>
 </div>
 <div className="mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs text-bamboo-200/60">
 <span>{actorLabel(t, e)}</span>
 {e.resourceType && <ResourcePill type={e.resourceType} id={e.resourceId} />}
 </div>
 </li>
 ))}
 </ol>
 );
}

function ResourcePill({ type, id }: { type: string; id?: string }) {
 // Resource pill:"<type>/<short-id>" in mono, light text on a
 // raised ink surface so it reads as metadata without competing
 // with the action label above.
 const short = id ? id.slice(0, 8) : '';
 return (
 <span className="rounded bg-ink-800 px-1.5 py-0.5 font-mono text-[10px] text-bamboo-100/80">
 {type}
 {short && <span className="ml-0.5 opacity-70">/{short}</span>}
 </span>
 );
}

// Whitelist of audit action / actor type strings that have matching
// messages JSON entries. Mirrors the pattern in PeerDrawer.tsx:
// next-intl 3.x throws on missing keys, so we must avoid calling
// t() with an unknown suffix or the whole feed crashes.
const KNOWN_ACTIONS = new Set(['peer.register', 'peer.update', 'peer.delete', 'policy.update']);
const KNOWN_ACTORS = new Set<ActivityEvent['actorType']>(['user', 'system', 'api']);

function actionLabel(t: ReturnType<typeof useTranslations>, action: string): string {
 if (KNOWN_ACTIONS.has(action)) {
 // next-intl treats"." as namespace nesting, so we can't key the
 // JSON with `peer.register` literally. The audit log on the wire
 // still uses dotted form; the i18n JSON uses `peer_register` and
 // we translate at lookup time.
 return t(`action.${action.replaceAll('.', '_')}` as never);
 }
 return action;
}

function actorLabel(t: ReturnType<typeof useTranslations>, e: ActivityEvent): string {
 if (e.actorType === 'user' && e.actorEmail) {
 return e.actorEmail;
 }
 if (KNOWN_ACTORS.has(e.actorType)) {
 return t(`actor.${e.actorType}` as never);
 }
 return e.actorType;
}

function formatRelative(iso: string): string {
 const ms = Date.now() - new Date(iso).getTime();
 if (!Number.isFinite(ms) || ms < 0) return '—';
 const s = Math.round(ms / 1000);
 if (s < 60) return `${s}s`;
 if (s < 3600) return `${Math.round(s / 60)}m`;
 if (s < 86_400) return `${Math.round(s / 3600)}h`;
 return `${Math.round(s / 86_400)}d`;
}
