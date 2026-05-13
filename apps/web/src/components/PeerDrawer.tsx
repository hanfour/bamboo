// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useRef, useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import {
  deletePeerAction,
  renamePeerAction,
  setPeerStatusAction,
  setPeerTagsAction,
} from '@/lib/actions';
import { useDialogA11y } from '@/hooks/useDialogA11y';
import type { FetchResult, Peer, PeerEvent } from '@/lib/types';

type Props = {
  // peerResult is null when nothing is selected (drawer closed) and
  // a FetchResult otherwise. The three FetchResult variants drive
  // three distinct render states inside the drawer body:
  //   - kind='ok'       → full peer detail (DrawerBody)
  //   - kind='notFound' → peer deleted / cross-tenant / bad uuid
  //   - kind='error'    → controller unreachable / 5xx — distinct
  //     from notFound so the user doesn't read "node was deleted"
  //     when the real problem is a network outage.
  peerResult: FetchResult<Peer> | null;
  events: PeerEvent[];
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
export function PeerDrawer({ peerResult, events, open, onClose, onDeleted }: Props) {
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
        className={`absolute inset-0 bg-zinc-900/40 transition-opacity dark:bg-black/60 ${
          open ? 'opacity-100' : 'opacity-0'
        }`}
        onClick={onClose}
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="peer-drawer-title"
        className={`absolute right-0 top-0 flex h-full w-full max-w-md flex-col border-l border-zinc-200 bg-white shadow-xl transition-transform dark:border-zinc-800 dark:bg-zinc-950 ${
          open ? 'translate-x-0' : 'translate-x-full'
        }`}
      >
        <DrawerHeader peer={peer} statusLabel={peer ? tStatus(peer.status) : ''} onClose={onClose} closeLabel={t('close')} />
        <div className="flex-1 overflow-y-auto px-6 py-4">
          {renderBody(peerResult, events, onDeleted, t)}
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
    <header className="flex items-start justify-between border-b border-zinc-200 px-6 py-4 dark:border-zinc-800">
      <div className="min-w-0">
        <h2
          id="peer-drawer-title"
          className="truncate text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-100"
        >
          {peer?.hostname ?? '—'}
        </h2>
        {peer && (
          <div className="mt-1 flex items-center gap-2">
            <StatusBadge status={peer.status} label={statusLabel} />
            <span className="font-mono text-xs text-zinc-500 dark:text-zinc-400">{peer.ip}</span>
          </div>
        )}
      </div>
      <button
        type="button"
        onClick={onClose}
        aria-label={closeLabel}
        className="-mr-2 -mt-1 rounded p-2 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
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
  onDeleted,
}: {
  peer: Peer;
  events: PeerEvent[];
  onDeleted: () => void;
}) {
  const t = useTranslations('peers.drawer');
  const [error, setError] = useState<string | null>(null);

  return (
    <div className="space-y-6">
      {error && (
        <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/40 dark:text-red-300">
          {t('errorPrefix')} {error}
        </div>
      )}

      <Section title={t('sections.basic')}>
        <HostnameField peer={peer} onError={setError} />
        <Field label={t('fields.ip')} value={peer.ip} mono />
        <Field label={t('fields.os')} value={peer.os || '—'} />
        <Field label={t('fields.clientVersion')} value={peer.clientVersion || '—'} mono />
        <TagsField peer={peer} onError={setError} />
      </Section>

      <Section title={t('sections.wireguard')}>
        <Field
          label={t('fields.publicKey')}
          value={
            peer.wireguardPublicKey ? (
              <code className="block break-all rounded bg-zinc-100 px-2 py-1 font-mono text-xs text-zinc-800 dark:bg-zinc-900 dark:text-zinc-200">
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
          <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('empty.noHandshake')}</p>
        )}
      </Section>

      <Section title={t('sections.endpoints')}>
        {peer.endpoints.length === 0 ? (
          <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('empty.endpoints')}</p>
        ) : (
          <ul className="space-y-1">
            {peer.endpoints.map((ep) => (
              <li
                key={ep}
                className="rounded bg-zinc-100 px-2 py-1 font-mono text-xs text-zinc-800 dark:bg-zinc-900 dark:text-zinc-200"
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

      <Section title={t('sections.actions')}>
        <DisableToggle peer={peer} onError={setError} />
        <DeleteButton peer={peer} onError={setError} onDeleted={onDeleted} />
      </Section>

      <Section title={t('sections.timeline')}>
        <Timeline events={events} />
      </Section>
    </div>
  );
}

// HostnameField wraps the basic-info hostname row with click-to-edit
// behavior: shows the value with a small "edit" affordance until
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
      <dt className="text-zinc-500 dark:text-zinc-400">{t('fields.hostname')}</dt>
      <dd className="min-w-0 break-words text-zinc-900 dark:text-zinc-100">
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
              className="flex-1 rounded border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900 outline-none focus:border-bamboo-500 focus:ring-1 focus:ring-bamboo-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
            />
            <button
              type="button"
              onClick={save}
              disabled={pending}
              className="rounded border border-bamboo-600 bg-bamboo-600 px-2 text-xs font-medium text-white hover:bg-bamboo-700 disabled:opacity-50"
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
              className="rounded border border-zinc-300 px-2 text-xs text-zinc-700 hover:bg-zinc-50 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-800"
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
            <span className="text-xs text-zinc-400 opacity-0 group-hover:opacity-100">
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
      <dt className="text-zinc-500 dark:text-zinc-400">{t('fields.tags')}</dt>
      <dd className="min-w-0 break-words text-zinc-900 dark:text-zinc-100">
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
            className="w-full rounded border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900 outline-none focus:border-bamboo-500 focus:ring-1 focus:ring-bamboo-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
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
              <span className="text-zinc-500 dark:text-zinc-400">{t('empty.tags')}</span>
            ) : (
              peer.tags.map((tag) => (
                <span
                  key={tag}
                  className="rounded bg-zinc-100 px-2 py-0.5 text-xs text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300"
                >
                  {tag}
                </span>
              ))
            )}
            <span className="text-xs text-zinc-400 opacity-0 group-hover:opacity-100">✎</span>
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
      className="w-full rounded-md border border-zinc-200 px-3 py-1.5 text-left text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-50 dark:border-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-900"
    >
      {pending ? t('inline.working') : label}
    </button>
  );
}

// DeleteButton uses a two-stage confirm pattern instead of a modal:
// first click reveals "確定 / 取消" buttons inline; second click on
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
            className="flex-1 rounded-md border border-red-600 bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
          >
            {pending ? t('inline.working') : t('actions.confirmYes')}
          </button>
          <button
            type="button"
            disabled={pending}
            onClick={() => setConfirming(false)}
            className="flex-1 rounded-md border border-zinc-200 px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-50 dark:border-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-900"
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
      className="w-full rounded-md border border-red-200 px-3 py-1.5 text-left text-sm text-red-700 hover:bg-red-50 dark:border-red-900/50 dark:text-red-400 dark:hover:bg-red-950/40"
    >
      {t('actions.delete')}
    </button>
  );
}

// Timeline renders the per-peer audit log newest-first. Each entry
// shows action label + actor + relative time, with an inline diff
// summary tuned to the action's diff shape:
//   - peer.update: per-field "key: from → to" lines (the only diff
//     shape the controller actively renames toward field-level)
//   - peer.register / peer.delete: small key: value list of the
//     attribute snapshot the controller wrote
// Unknown actions or malformed diffs fall back to pretty JSON.
function Timeline({ events }: { events: PeerEvent[] }) {
  const t = useTranslations('peers.drawer');
  if (events.length === 0) {
    return <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('empty.timeline')}</p>;
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
          <div className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">
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
    // next-intl treats "." as namespace nesting, so we can't key the
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
              <span className="font-medium text-zinc-600 dark:text-zinc-400">{key}:</span>
              <code className="rounded bg-zinc-100 px-1 font-mono text-zinc-700 dark:bg-zinc-900 dark:text-zinc-300">
                {formatDiffValue(ft.from)}
              </code>
              <span className="text-zinc-400">→</span>
              <code className="rounded bg-bamboo-50 px-1 font-mono text-bamboo-800 dark:bg-bamboo-900/30 dark:text-bamboo-200">
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
          <span className="text-zinc-500 dark:text-zinc-400">{key}:</span>
          <code className="rounded bg-zinc-100 px-1 font-mono text-zinc-700 dark:bg-zinc-900 dark:text-zinc-300">
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

// formatRelative produces "3m ago" style strings. Granularity is
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
  onDeleted: () => void,
  t: ReturnType<typeof useTranslations>,
) {
  if (peerResult === null) return null;
  switch (peerResult.kind) {
    case 'ok':
      // key forces the body to remount on peer-change so the
      // inline edit / confirm state resets between selections.
      return <DrawerBody key={peerResult.value.id} peer={peerResult.value} events={events} onDeleted={onDeleted} />;
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
// gray of "expected miss" and the red of "something is wrong".
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
    tone === 'error' ? 'text-red-700 dark:text-red-300' : 'text-zinc-500 dark:text-zinc-400';
  const colorDetail =
    tone === 'error' ? 'text-red-600 dark:text-red-400' : 'text-zinc-400 dark:text-zinc-500';
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
      <h3 className="text-xs font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
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
      <dt className="text-zinc-500 dark:text-zinc-400">{label}</dt>
      <dd className={`min-w-0 break-words text-zinc-900 dark:text-zinc-100 ${mono ? 'font-mono text-xs' : ''}`}>
        {value}
      </dd>
    </div>
  );
}

function StatusBadge({ status, label }: { status: Peer['status']; label: string }) {
  const tone = {
    online: 'bg-bamboo-100 text-bamboo-800 dark:bg-bamboo-900/40 dark:text-bamboo-300',
    offline: 'bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400',
    disabled: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
  }[status];
  return <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${tone}`}>{label}</span>;
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
