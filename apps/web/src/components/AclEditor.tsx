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
// - Disable Save while pending
// - On success: switch back to read mode; the page re-renders with
// the new revision via revalidatePath('/[locale]/acl')
// - On 400 (parse error): keep edit mode open, show the controller's
// parser message inline near the textarea
// - On 409 (stale revision): show a"someone else saved" hint and
// ask the operator to cancel + reload
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
 <h2 className="text-sm font-medium uppercase tracking-wide text-bamboo-200/60">
 {t('viewSource')}
 </h2>
 <button
 type="button"
 onClick={() => {
 setDraft(hclSource);
 setEditing(true);
 }}
 className="rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm font-medium text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 dark:text-bamboo-100 dark:hover:text-bamboo-50"
 >
 {t('edit')}
 </button>
 </div>
 <pre className="overflow-x-auto rounded-lg border border-ink-800 p-4 font-mono text-xs leading-relaxed text-bamboo-50 dark:text-bamboo-100">
 <code>{hclSource}</code>
 </pre>
 </section>
 );
 }

 return (
 <section className="space-y-2">
 <div className="flex items-baseline justify-between gap-2">
 <h2 className="text-sm font-medium uppercase tracking-wide text-bamboo-200/60">
 {t('viewSource')}
 </h2>
 <div className="flex items-center gap-2">
 <button
 type="button"
 onClick={cancel}
 disabled={pending}
 className="rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50 disabled:opacity-50 dark:text-bamboo-100 dark:hover:text-bamboo-50"
 >
 {tEdit('cancel')}
 </button>
 <button
 type="button"
 onClick={save}
 disabled={pending || draft === hclSource}
 className="rounded-md bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-ink-800 hover:bg-ink-800 dark:text-bamboo-50"
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
 className="w-full min-h-[20rem] rounded-lg border border-bamboo-200/30 bg-ink-950 px-4 py-3 font-mono text-xs leading-relaxed text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300 dark:bg-ink-900 dark:text-bamboo-50"
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
