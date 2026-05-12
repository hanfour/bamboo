// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import type { ActivityEvent } from '@/lib/types';

// ActivityFeed renders the tenant-wide audit log on the dashboard.
// Layout: vertical timeline rail (hairline) with each event as a row
// containing a small dot marker, the action label, a relative-time
// mono badge, and the actor + resource pill on a second line.
// Mirrors the per-peer Timeline (PeerDrawer.tsx) but its own
// component because the i18n namespace differs.
export function ActivityFeed({ events }: { events: ActivityEvent[] }) {
  const t = useTranslations('dashboard.activity');
  if (events.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-white/[0.08] px-6 py-10 text-center text-sm text-zinc-500">
        {t('empty')}
      </p>
    );
  }
  return (
    <ol className="relative space-y-0">
      <span
        aria-hidden
        className="absolute bottom-2 left-[7px] top-2 w-px bg-white/[0.06]"
      />
      {events.map((e, idx) => (
        <li key={e.id} className="group relative pl-6 py-3">
          <span
            aria-hidden
            className="absolute left-[3px] top-[18px] h-2 w-2 rounded-full bg-zinc-700 ring-4 ring-ink-950 group-hover:bg-bamboo-400"
          />
          <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <span className="text-sm text-zinc-100">
              {actionLabel(t, e.action)}
            </span>
            <span className="font-mono text-[10px] uppercase tracking-[0.20em] text-zinc-500">
              {formatRelative(e.occurredAt)}
            </span>
          </div>
          <div className="mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-[11px] text-zinc-500">
            <span>{actorLabel(t, e)}</span>
            {e.resourceType && (
              <ResourcePill type={e.resourceType} id={e.resourceId} />
            )}
          </div>
          {idx < events.length - 1 ? (
            <span aria-hidden className="sr-only" />
          ) : null}
        </li>
      ))}
    </ol>
  );
}

function ResourcePill({ type, id }: { type: string; id?: string }) {
  const short = id ? id.slice(0, 8) : '';
  return (
    <span className="rounded border border-white/[0.06] px-1.5 py-0.5 font-mono text-[10px] text-zinc-400">
      {type}
      {short && <span className="ml-0.5 text-zinc-600">/{short}</span>}
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
