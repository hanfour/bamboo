// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import {
  rollbackPolicyAction,
  setPolicyAction,
  simulatePolicyAction,
  validatePolicyAction,
} from '@/lib/actions';
import { lineDiff, type DiffRow } from '@/lib/lineDiff';
import type { PolicyHistoryRow, PolicySimulation } from '@/lib/types';

// AclEditor is the issue-#134 tabbed editor for the tenant ACL.
// Three tabs:
//
//   Source   — read-only HCL view; click Edit to switch to an
//              inline textarea with Validate / Save / Cancel.
//   Preview  — run the controller's simulator against the current
//              draft (or the saved policy when not editing) and
//              render the allow/deny matrix across approved peers.
//   Versions — list past revisions with timestamps + applier and a
//              Rollback button per row.
//
// Why no CodeMirror: bundle cost vs traffic. The HCL surface is
// rarely-touched admin tooling; a monospace textarea + Validate +
// Simulator covers the editor's job without dragging in ~300KB of
// JS. If admin usage grows we can upgrade to CodeMirror as a
// follow-up.
type Tab = 'source' | 'preview' | 'versions';

export function AclEditor({
  hclSource,
  revision,
  revisions,
}: {
  hclSource: string;
  revision: number;
  // Server-rendered at page-load time so the Versions tab renders
  // synchronously when the operator clicks it. Empty array when
  // the revisions fetch failed or the user lacks admin permission —
  // the tab still renders, just with the "no history" empty state.
  revisions: PolicyHistoryRow[];
}) {
  const [tab, setTab] = useState<Tab>('source');
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(hclSource);

  return (
    <section className="space-y-4">
      <TabBar
        tab={tab}
        onChange={setTab}
        editing={editing}
        versionCount={revisions.length}
      />
      {tab === 'source' && (
        <SourceTab
          hclSource={hclSource}
          revision={revision}
          editing={editing}
          setEditing={setEditing}
          draft={draft}
          setDraft={setDraft}
        />
      )}
      {tab === 'preview' && (
        <PreviewTab hclSource={editing ? draft : hclSource} editing={editing} />
      )}
      {tab === 'versions' && (
        <VersionsTab
          revisions={revisions}
          currentRevision={revision}
          currentHclSource={hclSource}
        />
      )}
    </section>
  );
}

function TabBar({
  tab,
  onChange,
  editing,
  versionCount,
}: {
  tab: Tab;
  onChange: (t: Tab) => void;
  editing: boolean;
  versionCount: number;
}) {
  const t = useTranslations('acl.tabs');
  // Tabs read as ghost buttons; the active one carries a 2px bottom
  // stripe (same affordance the sidebar active item uses) and the
  // bamboo-50 text the rest of the page already uses for primary
  // content. The "editing" pill next to Source nudges the operator
  // away from accidentally clicking Preview before they save.
  const items: { id: Tab; label: string; badge?: string }[] = [
    { id: 'source', label: t('source'), badge: editing ? t('editing') : undefined },
    { id: 'preview', label: t('preview') },
    { id: 'versions', label: t('versions') + (versionCount ? ` · ${versionCount}` : '') },
  ];
  return (
    <div className="flex items-end gap-6 border-b border-ink-800">
      {items.map((it) => {
        const active = it.id === tab;
        return (
          <button
            key={it.id}
            type="button"
            onClick={() => onChange(it.id)}
            className={[
              'relative pb-2 text-sm font-medium transition-colors',
              active
                ? 'text-bamboo-50 before:absolute before:inset-x-0 before:-bottom-px before:h-0.5 before:rounded-full before:bg-bamboo-400'
                : 'text-bamboo-200/60 hover:text-bamboo-50',
            ].join(' ')}
          >
            {it.label}
            {it.badge && (
              <span className="ml-2 rounded bg-bamboo-300/15 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-bamboo-300">
                {it.badge}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}

function SourceTab({
  hclSource,
  revision,
  editing,
  setEditing,
  draft,
  setDraft,
}: {
  hclSource: string;
  revision: number;
  editing: boolean;
  setEditing: (e: boolean) => void;
  draft: string;
  setDraft: (s: string) => void;
}) {
  const tEdit = useTranslations('acl.editor');
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<{ msg: string; stale: boolean } | null>(null);
  const [validation, setValidation] = useState<{ ok: boolean; rules?: number; error?: string } | null>(null);

  function validate() {
    setError(null);
    setValidation(null);
    startTransition(async () => {
      const r = await validatePolicyAction(draft);
      setValidation(r.ok ? { ok: true, rules: r.rules } : { ok: false, error: r.error });
    });
  }

  function save() {
    setError(null);
    startTransition(async () => {
      const res = await setPolicyAction({ hclSource: draft, expectedRevision: revision });
      if (res.ok) {
        setEditing(false);
        setValidation(null);
      } else {
        setError({ msg: res.error, stale: Boolean(res.staleRevision) });
      }
    });
  }

  function cancel() {
    setEditing(false);
    setDraft(hclSource);
    setError(null);
    setValidation(null);
  }

  if (!editing) {
    return (
      <div className="space-y-2">
        <div className="flex items-center justify-end">
          <button
            type="button"
            onClick={() => setEditing(true)}
            className="rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm font-medium text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50"
          >
            {tEdit('edit')}
          </button>
        </div>
        <pre className="overflow-x-auto rounded-lg border border-ink-800 p-4 font-mono text-xs leading-relaxed text-bamboo-50">
          <code>{hclSource}</code>
        </pre>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={cancel}
          disabled={pending}
          className="rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 disabled:opacity-50"
        >
          {tEdit('cancel')}
        </button>
        <button
          type="button"
          onClick={validate}
          disabled={pending}
          className="rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 disabled:opacity-50"
        >
          {tEdit('validate')}
        </button>
        <button
          type="button"
          onClick={save}
          disabled={pending || draft === hclSource}
          className="rounded-md border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-ink-950 transition-colors hover:bg-bamboo-100 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {pending ? tEdit('working') : tEdit('save')}
        </button>
      </div>
      <textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        spellCheck={false}
        disabled={pending}
        placeholder={tEdit('placeholder')}
        className="w-full min-h-[20rem] rounded-lg border border-bamboo-200/30 bg-ink-950 px-4 py-3 font-mono text-xs leading-relaxed text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
      />
      {validation && validation.ok && (
        <div className="rounded-md border border-bamboo-300/30 px-3 py-2 text-sm text-bamboo-200/80">
          {tEdit('validateOk', { rules: validation.rules ?? 0 })}
        </div>
      )}
      {validation && !validation.ok && (
        <div className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400">
          {tEdit('errorPrefix')}{' '}
          <code className="font-mono text-xs">{validation.error}</code>
        </div>
      )}
      {error && (
        <div className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400">
          {error.stale ? (
            tEdit('staleRevision')
          ) : (
            <>
              {tEdit('errorPrefix')} <code className="font-mono text-xs">{error.msg}</code>
            </>
          )}
        </div>
      )}
    </div>
  );
}

function PreviewTab({ hclSource, editing }: { hclSource: string; editing: boolean }) {
  const t = useTranslations('acl.preview');
  const [state, setState] = useState<
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'ok'; simulation: PolicySimulation }
    | { kind: 'error'; message: string }
  >({ kind: 'idle' });
  const [, startTransition] = useTransition();

  function run() {
    setState({ kind: 'loading' });
    startTransition(async () => {
      const r = await simulatePolicyAction(hclSource);
      if (r.ok) {
        setState({ kind: 'ok', simulation: r.simulation });
      } else {
        setState({ kind: 'error', message: r.error });
      }
    });
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-bamboo-200/70">
          {editing ? t('descDraft') : t('descSaved')}
        </p>
        <button
          type="button"
          onClick={run}
          disabled={state.kind === 'loading'}
          className="rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm font-medium text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 disabled:opacity-50"
        >
          {state.kind === 'loading' ? t('working') : t('run')}
        </button>
      </div>

      {state.kind === 'idle' && (
        <p className="rounded-md border border-dashed border-ink-700 p-6 text-center text-sm text-bamboo-200/60">
          {t('idle')}
        </p>
      )}
      {state.kind === 'error' && (
        <div className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400">
          {t('error')}: <code className="font-mono text-xs">{state.message}</code>
        </div>
      )}
      {state.kind === 'ok' && (
        <SimulationMatrix simulation={state.simulation} />
      )}
    </div>
  );
}

function SimulationMatrix({ simulation }: { simulation: PolicySimulation }) {
  const t = useTranslations('acl.preview');
  // Build the unique peer set in column order (sorted by hostname for
  // deterministic rendering). Then for each row, look up the verdict
  // in a map keyed by `${src}->${dst}`.
  const peerSet = new Map<string, string>();
  for (const e of simulation.edges) {
    peerSet.set(e.sourceId, e.sourceHostname);
    peerSet.set(e.destId, e.destHostname);
  }
  const peers = Array.from(peerSet.entries()).sort((a, b) => a[1].localeCompare(b[1]));
  const edgeMap = new Map(simulation.edges.map((e) => [`${e.sourceId}->${e.destId}`, e]));

  if (peers.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-ink-700 p-6 text-center text-sm text-bamboo-200/60">
        {t('emptyPeers')}
      </p>
    );
  }

  return (
    <div className="space-y-2">
      <p className="text-xs text-bamboo-200/60">
        {t('summary', {
          rules: simulation.rules,
          edges: simulation.edges.length,
          allowed: simulation.edges.filter((e) => e.allow).length,
        })}
      </p>
      <div className="overflow-x-auto">
        <table className="border-collapse text-xs">
          <thead>
            <tr>
              <th className="border border-ink-800 bg-ink-900/50 p-2 text-left font-medium text-bamboo-200/60">
                {t('matrixHeader')}
              </th>
              {peers.map(([id, name]) => (
                <th
                  key={id}
                  className="border border-ink-800 bg-ink-900/50 p-2 font-medium text-bamboo-200/80"
                  title={id}
                >
                  {name}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {peers.map(([srcId, srcName]) => (
              <tr key={srcId}>
                <th
                  className="border border-ink-800 bg-ink-900/50 p-2 text-left font-medium text-bamboo-200/80"
                  title={srcId}
                >
                  {srcName}
                </th>
                {peers.map(([dstId]) => {
                  if (srcId === dstId) {
                    return (
                      <td
                        key={dstId}
                        className="border border-ink-800 p-2 text-center text-bamboo-200/30"
                      >
                        —
                      </td>
                    );
                  }
                  const edge = edgeMap.get(`${srcId}->${dstId}`);
                  const allow = edge?.allow ?? false;
                  return (
                    <td
                      key={dstId}
                      className={[
                        'border border-ink-800 p-2 text-center text-xs font-medium',
                        allow ? 'text-bamboo-300' : 'text-red-400/70',
                      ].join(' ')}
                      title={
                        edge?.allowedIps && edge.allowedIps.length > 0
                          ? edge.allowedIps.join(', ')
                          : undefined
                      }
                    >
                      {allow ? '✓' : '✗'}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function VersionsTab({
  revisions,
  currentRevision,
  currentHclSource,
}: {
  revisions: PolicyHistoryRow[];
  currentRevision: number;
  currentHclSource: string;
}) {
  const t = useTranslations('acl.versions');
  const [busyRev, setBusyRev] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [, startTransition] = useTransition();
  // Each row can independently expand into Source OR Diff. We track
  // them as a (rev, mode) pair rather than two booleans so toggling
  // between modes on the same row stays click-cheap (the panel
  // re-renders rather than collapsing + re-expanding).
  const [expanded, setExpanded] = useState<{ rev: number; mode: 'source' | 'diff' } | null>(null);

  function rollback(rev: number) {
    setError(null);
    setBusyRev(rev);
    startTransition(async () => {
      const r = await rollbackPolicyAction(rev);
      setBusyRev(null);
      if (!r.ok) {
        setError(r.error);
      }
    });
  }

  if (revisions.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-ink-700 p-6 text-center text-sm text-bamboo-200/60">
        {t('empty')}
      </p>
    );
  }

  return (
    <div className="space-y-3">
      <p className="text-sm text-bamboo-200/70">{t('description')}</p>
      {error && (
        <div className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400">
          {error}
        </div>
      )}
      <ol className="divide-y divide-ink-800 rounded-lg border border-ink-800">
        {revisions.map((r) => {
          const isCurrent = r.revision === currentRevision;
          const isSourceOpen = expanded?.rev === r.revision && expanded.mode === 'source';
          const isDiffOpen = expanded?.rev === r.revision && expanded.mode === 'diff';
          const sameAsCurrent = r.hclSource === currentHclSource;

          function toggle(mode: 'source' | 'diff') {
            if (expanded?.rev === r.revision && expanded.mode === mode) {
              setExpanded(null);
            } else {
              setExpanded({ rev: r.revision, mode });
            }
          }

          return (
            <li key={r.revision} className="space-y-2 p-4">
              <div className="flex flex-wrap items-baseline justify-between gap-3">
                <div className="space-y-1">
                  <div className="flex items-baseline gap-3">
                    <span className="font-mono text-sm text-bamboo-50">
                      r{r.revision}
                    </span>
                    {isCurrent && (
                      <span className="rounded bg-bamboo-300/15 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-bamboo-300">
                        {t('current')}
                      </span>
                    )}
                  </div>
                  <div className="text-xs text-bamboo-200/60">
                    <time dateTime={r.appliedAt}>{new Date(r.appliedAt).toLocaleString()}</time>
                    {r.appliedByEmail && (
                      <>
                        {' · '}
                        <span>{r.appliedByEmail}</span>
                      </>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => toggle('source')}
                    className="rounded border border-ink-700 px-2.5 py-1 text-xs text-bamboo-200/80 transition-colors hover:bg-ink-800 hover:text-bamboo-50"
                  >
                    {isSourceOpen ? t('hideSource') : t('viewSource')}
                  </button>
                  {!isCurrent && (
                    <button
                      type="button"
                      onClick={() => toggle('diff')}
                      disabled={sameAsCurrent}
                      title={sameAsCurrent ? t('rollbackSameAsCurrent') : undefined}
                      className="rounded border border-ink-700 px-2.5 py-1 text-xs text-bamboo-200/80 transition-colors hover:bg-ink-800 hover:text-bamboo-50 disabled:cursor-not-allowed disabled:opacity-50"
                    >
                      {isDiffOpen ? t('hideDiff') : t('viewDiff')}
                    </button>
                  )}
                  {!isCurrent && (
                    <button
                      type="button"
                      onClick={() => rollback(r.revision)}
                      disabled={busyRev !== null || sameAsCurrent}
                      title={sameAsCurrent ? t('rollbackSameAsCurrent') : undefined}
                      className="rounded border border-bamboo-300/40 bg-bamboo-50 px-2.5 py-1 text-xs font-medium text-ink-950 transition-colors hover:bg-bamboo-100 disabled:cursor-not-allowed disabled:opacity-50"
                    >
                      {busyRev === r.revision ? t('rollingBack') : t('rollback')}
                    </button>
                  )}
                </div>
              </div>
              {isSourceOpen && (
                <pre className="overflow-x-auto rounded border border-ink-800 p-3 font-mono text-[11px] leading-relaxed text-bamboo-100">
                  <code>{r.hclSource}</code>
                </pre>
              )}
              {isDiffOpen && (
                <DiffView
                  oldText={r.hclSource}
                  newText={currentHclSource}
                  oldLabel={t('diffOldLabel', { revision: r.revision })}
                  newLabel={t('diffNewLabel', { revision: currentRevision })}
                />
              )}
            </li>
          );
        })}
      </ol>
    </div>
  );
}

// DiffView renders a side-by-side line-by-line comparison of two HCL
// blobs. The Versions tab pipes (historical revision → current) here
// so an operator can answer "what did r5 → r7 actually change?"
// without copy-pasting into an external diff tool.
//
// Visual conventions:
//   * Left column = historical (old). Right column = current (new).
//   * Removed lines: left filled with red tint, right blank.
//   * Added lines: right filled with green tint, left blank.
//   * Same lines: both columns show the line in muted gray.
//
// Gutter line numbers are 1-based; "same" rows show both, "add" / "remove"
// rows show only the side that has content. The empty-on-both-sides
// case (two empty inputs) renders an explanatory line rather than an
// empty pane so the section doesn't look broken.
function DiffView({
  oldText,
  newText,
  oldLabel,
  newLabel,
}: {
  oldText: string;
  newText: string;
  oldLabel: string;
  newLabel: string;
}) {
  const t = useTranslations('acl.versions');
  const rows = lineDiff(oldText, newText);
  if (rows.length === 0) {
    return (
      <p className="rounded border border-ink-800 p-3 text-xs text-bamboo-200/60">
        {t('diffEmpty')}
      </p>
    );
  }
  return (
    <div className="overflow-hidden rounded border border-ink-800 font-mono text-[11px] leading-relaxed">
      <div className="grid grid-cols-2 divide-x divide-ink-800 border-b border-ink-800 bg-ink-900/60 text-[10px] uppercase tracking-wide text-bamboo-200/60">
        <div className="px-3 py-1.5">{oldLabel}</div>
        <div className="px-3 py-1.5">{newLabel}</div>
      </div>
      <div className="grid grid-cols-2 divide-x divide-ink-800 overflow-x-auto">
        <div>
          {rows.map((row, i) => (
            <DiffCell key={`L-${i}`} side="old" row={row} />
          ))}
        </div>
        <div>
          {rows.map((row, i) => (
            <DiffCell key={`R-${i}`} side="new" row={row} />
          ))}
        </div>
      </div>
    </div>
  );
}

// DiffCell renders one side of one diff row. Kept as its own
// component so the empty-side states (a "remove" row on the right,
// or an "add" row on the left) get a consistent vertical rhythm
// without rendering an actual placeholder line — the absence is the
// signal.
function DiffCell({ side, row }: { side: 'old' | 'new'; row: DiffRow }) {
  // Empty side: render a blank background-tinted row so the two
  // columns stay aligned. Without this the columns would drift in
  // height as one side has more content rows.
  const empty =
    (side === 'old' && row.kind === 'add') || (side === 'new' && row.kind === 'remove');
  if (empty) {
    return <div className="h-[1.65em] bg-ink-900/40" aria-hidden="true" />;
  }
  const text = side === 'old' ? row.oldLine ?? '' : row.newLine ?? '';
  const lineNo = side === 'old' ? row.oldLineNumber : row.newLineNumber;
  let bg = '';
  let prefix = ' ';
  if (row.kind === 'remove') {
    bg = 'bg-red-900/25 text-red-100';
    prefix = '-';
  } else if (row.kind === 'add') {
    bg = 'bg-emerald-900/25 text-emerald-100';
    prefix = '+';
  } else {
    bg = 'text-bamboo-200/80';
  }
  return (
    <div className={`flex items-baseline gap-2 px-3 ${bg}`}>
      <span
        aria-hidden="true"
        className="select-none text-[10px] text-bamboo-200/40 tabular-nums w-6 text-right"
      >
        {lineNo ?? ''}
      </span>
      <span className="select-none text-[10px] text-bamboo-200/40 w-3">{prefix}</span>
      <pre className="flex-1 whitespace-pre-wrap break-words text-[11px] leading-relaxed">
        {text || ' '}
      </pre>
    </div>
  );
}
