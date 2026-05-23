// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import {
  createWebhookAction,
  deleteWebhookAction,
  testWebhookAction,
  updateWebhookAction,
  type WebhookTestResult,
} from '@/lib/actions';
import type { Webhook } from '@/lib/types';

// WebhooksView is the §4 P2 webhook subscriptions admin surface.
// Layout:
//
//   ┌──────────────────────────────────────────────────────────┐
//   │ Add webhook (button → opens inline form)                 │
//   ├──────────────────────────────────────────────────────────┤
//   │ Existing subscriptions                                   │
//   │   row per Webhook: url, event types, active toggle,      │
//   │   Test / Delete buttons; secret reveal modal on create   │
//   └──────────────────────────────────────────────────────────┘
//
// State machine is intentionally light — a brand-new subscription's
// secret lives in the modal until the operator dismisses it, then
// is unrecoverable. Form errors stay inline; per-row Test feedback
// renders next to the button so the operator doesn't have to
// correlate a toast to a row.
export function WebhooksView({ hooks }: { hooks: Webhook[] }) {
  const t = useTranslations('webhooks');
  const [error, setError] = useState<string | null>(null);
  const [secret, setSecret] = useState<MintedSecret | null>(null);
  const [formOpen, setFormOpen] = useState(false);

  return (
    <div className="space-y-4">
      {error && (
        <div className="rounded-md border border-red-900/50 px-3 py-2 text-sm text-red-400">
          {t('errorPrefix')} {error}
        </div>
      )}

      <div className="flex justify-end">
        <button
          type="button"
          onClick={() => {
            setFormOpen((v) => !v);
            setError(null);
          }}
          className="rounded-md border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-ink-950 transition-colors hover:bg-bamboo-100"
        >
          {formOpen ? t('form.cancel') : t('form.openButton')}
        </button>
      </div>

      {formOpen && (
        <CreateForm
          onError={setError}
          onCreated={(s) => {
            setFormOpen(false);
            setSecret(s);
            setError(null);
          }}
        />
      )}

      {hooks.length === 0 ? (
        <p className="rounded-md border border-dashed border-ink-700 p-6 text-center text-sm text-bamboo-200/60">
          {t('empty')}
        </p>
      ) : (
        <ol className="divide-y divide-ink-800 rounded-lg border border-ink-800">
          {hooks.map((h) => (
            <WebhookRow key={h.id} hook={h} onError={setError} />
          ))}
        </ol>
      )}

      {secret && <SecretRevealModal mint={secret} onClose={() => setSecret(null)} />}
    </div>
  );
}

type MintedSecret = { id: string; url: string; secret: string };

// CreateForm is the inline new-subscription form. URL is required;
// eventTypes is optional + accepted as a comma-separated string for
// admin convenience. The controller validates per-entry grammar so
// we don't duplicate that check here — operators see the controller's
// diagnostic verbatim on a malformed entry.
function CreateForm({
  onError,
  onCreated,
}: {
  onError: (msg: string | null) => void;
  onCreated: (s: MintedSecret) => void;
}) {
  const t = useTranslations('webhooks.form');
  const [url, setUrl] = useState('');
  const [eventTypesRaw, setEventTypesRaw] = useState('');
  const [description, setDescription] = useState('');
  const [pending, startTransition] = useTransition();

  function submit() {
    onError(null);
    startTransition(async () => {
      const types = eventTypesRaw
        .split(',')
        .map((s) => s.trim())
        .filter((s) => s.length > 0);
      const res = await createWebhookAction({ url, eventTypes: types, description });
      if (res.ok) {
        onCreated({ id: res.id, url: url.trim(), secret: res.secret });
        setUrl('');
        setEventTypesRaw('');
        setDescription('');
      } else {
        onError(res.error);
      }
    });
  }

  return (
    <div className="space-y-3 rounded-md border border-ink-800 p-4">
      <FormField label={t('urlLabel')} hint={t('urlHint')}>
        <input
          type="url"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder={t('urlPlaceholder')}
          disabled={pending}
          className="w-full rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1.5 font-mono text-xs text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
        />
      </FormField>
      <FormField label={t('eventTypesLabel')} hint={t('eventTypesHint')}>
        <input
          type="text"
          value={eventTypesRaw}
          onChange={(e) => setEventTypesRaw(e.target.value)}
          placeholder={t('eventTypesPlaceholder')}
          disabled={pending}
          className="w-full rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1.5 font-mono text-xs text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
        />
      </FormField>
      <FormField label={t('descriptionLabel')}>
        <input
          type="text"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t('descriptionPlaceholder')}
          disabled={pending}
          className="w-full rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1.5 text-sm text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
        />
      </FormField>
      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={submit}
          disabled={pending || url.trim() === ''}
          className="rounded-md border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-ink-950 transition-colors hover:bg-bamboo-100 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {pending ? t('working') : t('submit')}
        </button>
      </div>
    </div>
  );
}

function FormField({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <label className="block text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-bamboo-200/50">{hint}</p>}
    </div>
  );
}

// WebhookRow is one subscription line. Three actions: toggle
// active, fire a synchronous Test, delete (with inline confirm).
// Test feedback renders next to the button so the operator can
// trace cause → outcome without a toast layer.
function WebhookRow({
  hook,
  onError,
}: {
  hook: Webhook;
  onError: (msg: string | null) => void;
}) {
  const t = useTranslations('webhooks');
  const [pending, startTransition] = useTransition();
  const [testResult, setTestResult] = useState<WebhookTestResult | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  function toggleActive() {
    onError(null);
    startTransition(async () => {
      const res = await updateWebhookAction(hook.id, { active: !hook.active });
      if (!res.ok) onError(res.error);
    });
  }

  function runTest() {
    onError(null);
    setTestResult(null);
    startTransition(async () => {
      const res = await testWebhookAction(hook.id);
      setTestResult(res);
      if (!res.ok) onError(res.error);
    });
  }

  function doDelete() {
    onError(null);
    startTransition(async () => {
      const res = await deleteWebhookAction(hook.id);
      if (!res.ok) onError(res.error);
    });
  }

  return (
    <li className="space-y-2 p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex items-baseline gap-2">
            <code className="break-all font-mono text-sm text-bamboo-50">{hook.url}</code>
            {!hook.active && (
              <span className="rounded bg-ink-800 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-bamboo-200/60">
                {t('inactiveBadge')}
              </span>
            )}
          </div>
          {hook.description && (
            <p className="text-xs text-bamboo-200/70">{hook.description}</p>
          )}
          <p className="text-xs text-bamboo-200/50">
            {hook.eventTypes.length === 0
              ? t('allEvents')
              : hook.eventTypes.join(', ')}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={runTest}
            disabled={pending}
            className="rounded border border-ink-700 px-2.5 py-1 text-xs text-bamboo-200/80 transition-colors hover:bg-ink-800 hover:text-bamboo-50 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {t('actions.test')}
          </button>
          <button
            type="button"
            onClick={toggleActive}
            disabled={pending}
            className="rounded border border-ink-700 px-2.5 py-1 text-xs text-bamboo-200/80 transition-colors hover:bg-ink-800 hover:text-bamboo-50 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {hook.active ? t('actions.disable') : t('actions.enable')}
          </button>
          {confirmDelete ? (
            <>
              <button
                type="button"
                onClick={doDelete}
                disabled={pending}
                className="rounded border border-red-900/50 bg-red-900/20 px-2.5 py-1 text-xs font-medium text-red-300 transition-colors hover:bg-red-900/30 disabled:opacity-50"
              >
                {t('actions.confirmDelete')}
              </button>
              <button
                type="button"
                onClick={() => setConfirmDelete(false)}
                disabled={pending}
                className="rounded border border-ink-700 px-2.5 py-1 text-xs text-bamboo-200/80 transition-colors hover:bg-ink-800 hover:text-bamboo-50"
              >
                {t('actions.cancel')}
              </button>
            </>
          ) : (
            <button
              type="button"
              onClick={() => setConfirmDelete(true)}
              disabled={pending}
              className="rounded border border-ink-700 px-2.5 py-1 text-xs text-bamboo-200/70 transition-colors hover:border-red-900/50 hover:text-red-400 disabled:opacity-50"
            >
              {t('actions.delete')}
            </button>
          )}
        </div>
      </div>
      {testResult && <TestResultBanner result={testResult} />}
    </li>
  );
}

// TestResultBanner renders the outcome of a synchronous /test
// probe. Three states: delivered (green), receiver responded with
// non-2xx (amber + status code), connection error (red + error
// string). Without the status differentiation, the operator can't
// tell "receiver is broken" from "receiver isn't reachable" — two
// very different debugging directions.
function TestResultBanner({ result }: { result: WebhookTestResult }) {
  const t = useTranslations('webhooks.test');
  if (!result.ok) {
    return (
      <div className="rounded-md border border-red-900/50 bg-red-900/20 px-3 py-2 text-xs text-red-300">
        {t('controllerError')} {result.error}
      </div>
    );
  }
  if (result.delivered) {
    return (
      <div className="rounded-md border border-emerald-900/50 bg-emerald-900/20 px-3 py-2 text-xs text-emerald-300">
        {t('delivered', { status: result.status })}
      </div>
    );
  }
  if (result.status > 0) {
    return (
      <div className="rounded-md border border-amber-900/50 bg-amber-900/20 px-3 py-2 text-xs text-amber-300">
        {t('non2xx', { status: result.status })}
      </div>
    );
  }
  return (
    <div className="rounded-md border border-red-900/50 bg-red-900/20 px-3 py-2 text-xs text-red-300">
      {t('transportError')} {result.error ?? ''}
    </div>
  );
}

// SecretRevealModal shows the freshly-minted HMAC secret once.
// Operator must copy it before clicking Done — there's no recovery
// path from the controller's side. Modal stays open until explicit
// dismissal so a misclick on the page background doesn't lose the
// secret.
function SecretRevealModal({ mint, onClose }: { mint: MintedSecret; onClose: () => void }) {
  const t = useTranslations('webhooks.secretModal');
  const [copied, setCopied] = useState(false);

  function copy() {
    void navigator.clipboard.writeText(mint.secret).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="webhook-secret-title"
      className="fixed inset-0 z-50 flex items-center justify-center bg-ink-950/80 p-4"
    >
      <div className="w-full max-w-lg space-y-4 rounded-lg border border-bamboo-200/30 bg-ink-950 p-6">
        <h2 id="webhook-secret-title" className="text-lg font-medium text-bamboo-50">
          {t('title')}
        </h2>
        <p className="text-sm text-bamboo-200/70">{t('description')}</p>
        <div className="space-y-1">
          <label className="block text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
            {t('urlLabel')}
          </label>
          <code className="block break-all rounded border border-ink-800 px-2 py-1.5 font-mono text-xs text-bamboo-100">
            {mint.url}
          </code>
        </div>
        <div className="space-y-1">
          <label className="block text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
            {t('secretLabel')}
          </label>
          <code className="block break-all rounded border border-amber-900/50 bg-amber-900/20 px-2 py-1.5 font-mono text-xs text-amber-100">
            {mint.secret}
          </code>
          <p className="text-xs text-amber-300/80">{t('warning')}</p>
        </div>
        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={copy}
            className="rounded-md border border-bamboo-200/30 px-3 py-1.5 text-sm text-bamboo-100 transition-colors hover:border-bamboo-200/60 hover:text-bamboo-50"
          >
            {copied ? t('copied') : t('copy')}
          </button>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-ink-950 transition-colors hover:bg-bamboo-100"
          >
            {t('done')}
          </button>
        </div>
      </div>
    </div>
  );
}
