// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { setPolicyAction } from '@/lib/actions';

// AclEditor wraps the HCL source view with an Edit / Save flow. Read
// mode is a <pre> that mirrors the prior server-rendered view; edit
// mode swaps in a <textarea> + Save / Cancel buttons + an inline
// error region.
//
// Deliberately NOT shipping CodeMirror or a similar editor: the
// bundle cost (~300KB) is hard to justify for an admin surface that
// rarely changes. A plain monospace textarea covers the use case
// (paste, light edit, save); syntax highlighting + preview tabs are
// P2 if the editor gets heavy traffic.
//
// Save flow:
//   - Disable Save while pending
//   - On success: switch back to read mode; the page re-renders with
//     the new revision via revalidatePath('/[locale]/acl')
//   - On 400 (parse error): keep edit mode open, show the controller's
//     parser message inline near the textarea
//   - On 409 (stale revision): show a "someone else saved" hint and
//     ask the operator to cancel + reload
export function AclEditor({
  hclSource,
  revision,
}: {
  hclSource: string;
  revision: number;
}) {
  const t = useTranslations('acl');
  const tEdit = useTranslations('acl.editor');
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(hclSource);
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<{ msg: string; stale: boolean } | null>(null);

  function save() {
    setError(null);
    startTransition(async () => {
      const res = await setPolicyAction({
        hclSource: draft,
        expectedRevision: revision,
      });
      if (res.ok) {
        setEditing(false);
      } else {
        setError({ msg: res.error, stale: Boolean(res.staleRevision) });
      }
    });
  }

  function cancel() {
    setEditing(false);
    setDraft(hclSource);
    setError(null);
  }

  if (!editing) {
    return (
      <section className="space-y-2">
        <div className="flex items-baseline justify-between gap-2">
          <h2 className="text-sm font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
            {t('viewSource')}
          </h2>
          <button
            type="button"
            onClick={() => {
              setDraft(hclSource);
              setEditing(true);
            }}
            className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:border-zinc-400 hover:text-zinc-900 dark:border-zinc-700 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:text-zinc-100"
          >
            {t('edit')}
          </button>
        </div>
        <pre className="overflow-x-auto rounded-lg border border-zinc-200 p-4 font-mono text-xs leading-relaxed text-zinc-800 dark:border-zinc-800 dark:text-zinc-200">
          <code>{hclSource}</code>
        </pre>
      </section>
    );
  }

  return (
    <section className="space-y-2">
      <div className="flex items-baseline justify-between gap-2">
        <h2 className="text-sm font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
          {t('viewSource')}
        </h2>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={cancel}
            disabled={pending}
            className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm text-zinc-700 transition-colors hover:border-zinc-400 hover:text-zinc-900 disabled:opacity-50 dark:border-zinc-700 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:text-zinc-100"
          >
            {tEdit('cancel')}
          </button>
          <button
            type="button"
            onClick={save}
            disabled={pending || draft === hclSource}
            className="rounded-md bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900 dark:hover:bg-zinc-300"
          >
            {pending ? tEdit('working') : tEdit('save')}
          </button>
        </div>
      </div>
      <textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        spellCheck={false}
        disabled={pending}
        placeholder={tEdit('placeholder')}
        className="w-full min-h-[20rem] rounded-lg border border-zinc-300 bg-white px-4 py-3 font-mono text-xs leading-relaxed text-zinc-900 outline-none focus:border-bamboo-500 focus:ring-1 focus:ring-bamboo-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
      />
      {error && (
        <div className="rounded-md border border-red-300 px-3 py-2 text-sm text-red-700 dark:border-red-900/50 dark:text-red-400">
          {error.stale ? (
            tEdit('staleRevision')
          ) : (
            <>
              {tEdit('errorPrefix')} <code className="font-mono text-xs">{error.msg}</code>
            </>
          )}
        </div>
      )}
    </section>
  );
}
