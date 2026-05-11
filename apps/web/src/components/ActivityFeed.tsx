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
    return <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('empty')}</p>;
  }
  return (
    <ol className="space-y-3">
      {events.map((e) => (
        <li
          key={e.id}
          className="rounded-md border border-zinc-200 px-3 py-2 dark:border-zinc-800"
        >
          <div className="flex items-baseline justify-between gap-2 text-sm">
            <span className="font-medium text-zinc-900 dark:text-zinc-100">
              {actionLabel(t, e.action)}
            </span>
            <span className="shrink-0 text-xs text-zinc-500 dark:text-zinc-400">
              {formatRelative(e.occurredAt)}
            </span>
          </div>
          <div className="mt-0.5 flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs text-zinc-500 dark:text-zinc-400">
            <span>{actorLabel(t, e)}</span>
            {e.resourceType && <ResourcePill type={e.resourceType} id={e.resourceId} />}
          </div>
        </li>
      ))}
    </ol>
  );
}

function ResourcePill({ type, id }: { type: string; id?: string }) {
  // Render the resource as "<type>/<short-id>" to keep cards
  // scannable. Full uuids are rarely useful at a glance; the click-
  // through (when added) will use the full id.
  const short = id ? id.slice(0, 8) : '';
  return (
    <span className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-[10px] text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300">
      {type}
      {short && <span className="ml-0.5 opacity-70">/{short}</span>}
    </span>
  );
}

// Whitelist of audit action / actor type strings that have matching
// messages JSON entries. Mirrors the pattern in PeerDrawer.tsx:
// next-intl 3.x throws on missing keys, so we must avoid calling
// t() with an unknown suffix or the whole feed crashes.
const KNOWN_ACTIONS = new Set([
  'peer.register',
  'peer.update',
  'peer.delete',
  'peer.heartbeat',
  'policy.update',
]);
const KNOWN_ACTORS = new Set<ActivityEvent['actorType']>(['user', 'system', 'api']);

function actionLabel(t: ReturnType<typeof useTranslations>, action: string): string {
  if (KNOWN_ACTIONS.has(action)) {
    return t(`action.${action}` as never);
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
