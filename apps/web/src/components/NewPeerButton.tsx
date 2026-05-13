// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useRef, useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { QRCodeSVG } from 'qrcode.react';
import { mintPreAuthKeyAction } from '@/lib/actions';
import { useDialogA11y } from '@/hooks/useDialogA11y';

// NewPeerButton wraps the /peers header's "新增節點" button with a
// modal that mints a pre-auth key and shows the connect info once.
// Two stages:
//   - 'form' — description + reusable checkbox + submit
//   - 'result' — controller URL / tenant slug / pre-auth key, each
//     with a copy button; Done closes the modal
//
// The plaintext secret is shown ONCE. The controller stores only a
// bcrypt hash, so a user who closes the modal without copying has
// to mint a new key.
export function NewPeerButton({ tenantSlug }: { tenantSlug: string }) {
  const t = useTranslations('peers.newPeer');
  const [open, setOpen] = useState(false);
  const [description, setDescription] = useState('');
  const [reusable, setReusable] = useState(false);
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<{ secret: string; description: string } | null>(null);

  function close() {
    setOpen(false);
    // Defer reset until the close transition has had a chance to
    // run, so users don't see the form briefly flash before the
    // modal hides.
    setTimeout(() => {
      setDescription('');
      setReusable(false);
      setError(null);
      setResult(null);
    }, 200);
  }

  function submit() {
    setError(null);
    startTransition(async () => {
      const res = await mintPreAuthKeyAction({ description, reusable });
      if (res.ok) {
        setResult({ secret: res.secret, description: res.description });
      } else {
        setError(res.error);
      }
    });
  }

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-md bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 dark:bg-zinc-100 dark:text-zinc-900 dark:hover:bg-zinc-300"
      >
        {t('button')}
      </button>
      {open && (
        <Modal onClose={close} title={result ? t('resultTitle') : t('formTitle')}>
          {result ? (
            <ResultView
              tenantSlug={tenantSlug}
              secret={result.secret}
              description={result.description}
              onDone={close}
            />
          ) : (
            <FormView
              description={description}
              reusable={reusable}
              pending={pending}
              error={error}
              onDescriptionChange={setDescription}
              onReusableChange={setReusable}
              onSubmit={submit}
              onCancel={close}
            />
          )}
        </Modal>
      )}
    </>
  );
}

// Modal is a tiny centered-dialog primitive — backdrop click + ESC
// close, focus moves to the panel on open. Inlined here because the
// drawer's slide-in pattern is the wrong fit for a transient form;
// extracting a shared Modal *component* is overkill until we have a
// third surface, but the a11y wiring is shared via useDialogA11y.
function Modal({
  onClose,
  title,
  children,
}: {
  onClose: () => void;
  title: string;
  children: React.ReactNode;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  // Modal is always rendered when mounted (the outer caller gates
  // mount on `open` state) — pass open=true unconditionally here.
  // trapTab keeps tab focus inside the form so users can't drift
  // out to background content.
  useDialogA11y({ open: true, onClose, panelRef, trapTab: true });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div
        className="absolute inset-0 bg-zinc-900/40 dark:bg-black/60"
        onClick={onClose}
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="relative w-full max-w-md rounded-lg border border-zinc-200 bg-white shadow-xl dark:border-zinc-800 dark:bg-zinc-950"
      >
        <header className="border-b border-zinc-200 px-5 py-3 dark:border-zinc-800">
          <h2 className="text-base font-semibold text-zinc-900 dark:text-zinc-100">{title}</h2>
        </header>
        <div className="px-5 py-4">{children}</div>
      </div>
    </div>
  );
}

function FormView({
  description,
  reusable,
  pending,
  error,
  onDescriptionChange,
  onReusableChange,
  onSubmit,
  onCancel,
}: {
  description: string;
  reusable: boolean;
  pending: boolean;
  error: string | null;
  onDescriptionChange: (v: string) => void;
  onReusableChange: (v: boolean) => void;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  const t = useTranslations('peers.newPeer');
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
      className="space-y-4"
    >
      <p className="text-sm text-zinc-600 dark:text-zinc-400">{t('formHint')}</p>

      <label className="block space-y-1 text-sm">
        <span className="text-zinc-700 dark:text-zinc-300">{t('descriptionLabel')}</span>
        <input
          type="text"
          value={description}
          disabled={pending}
          onChange={(e) => onDescriptionChange(e.target.value)}
          placeholder={t('descriptionPlaceholder')}
          className="w-full rounded border border-zinc-300 bg-white px-2 py-1.5 text-sm text-zinc-900 outline-none focus:border-bamboo-500 focus:ring-1 focus:ring-bamboo-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
        />
      </label>

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={reusable}
          disabled={pending}
          onChange={(e) => onReusableChange(e.target.checked)}
        />
        <span className="text-zinc-700 dark:text-zinc-300">{t('reusableLabel')}</span>
      </label>
      <p className="-mt-2 text-xs text-zinc-500 dark:text-zinc-400">{t('reusableHint')}</p>

      {error && (
        <div className="rounded-md border border-red-300 px-3 py-2 text-sm text-red-700 dark:border-red-900/50 dark:text-red-400">
          {t('error')} {error}
        </div>
      )}

      <div className="flex justify-end gap-2 pt-2">
        <button
          type="button"
          onClick={onCancel}
          disabled={pending}
          className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm text-zinc-700 transition-colors hover:border-zinc-400 hover:text-zinc-900 disabled:opacity-50 dark:border-zinc-700 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:text-zinc-100"
        >
          {t('cancel')}
        </button>
        <button
          type="submit"
          disabled={pending}
          className="rounded-md bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900 dark:hover:bg-zinc-300"
        >
          {pending ? t('working') : t('submit')}
        </button>
      </div>
    </form>
  );
}

function ResultView({
  tenantSlug,
  secret,
  description,
  onDone,
}: {
  tenantSlug: string;
  secret: string;
  description: string;
  onDone: () => void;
}) {
  const t = useTranslations('peers.newPeer');
  // window.location.origin is browser-only; this component is
  // 'use client' and only ever renders after the user clicks, so
  // window is available. SSR sees only an empty modal frame.
  const controllerURL = typeof window !== 'undefined' ? window.location.origin : '';

  // QR payload: a small JSON envelope the Apple/Android client
  // can scan to auto-fill its three connection fields. The "type"
  // tag lets the client identify a bamboo config QR vs an
  // arbitrary scan; bumping it (v2) is how we'd add fields
  // without breaking older clients. Stays well under the ~1 KB
  // limit a Version-10 QR comfortably renders at modal size.
  const qrPayload = JSON.stringify({
    type: 'bamboo.config.v1',
    controllerUrl: controllerURL,
    tenantSlug,
    preAuthKey: secret,
  });

  return (
    <div className="space-y-4">
      <p className="text-sm text-zinc-700 dark:text-zinc-300">
        {t('resultHint')}
      </p>
      {description && (
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          {t('descriptionLabel')}: <span className="font-medium">{description}</span>
        </p>
      )}

      <div className="flex flex-col items-center gap-2 rounded-md border border-zinc-200 bg-white px-4 py-4 dark:border-zinc-800 dark:bg-zinc-900">
        <div className="rounded-md bg-white p-2">
          <QRCodeSVG
            value={qrPayload}
            size={180}
            level="M"
            aria-label={t('qrLabel')}
          />
        </div>
        <p className="text-center text-xs text-zinc-500 dark:text-zinc-400">{t('qrHint')}</p>
      </div>

      <details className="space-y-2 text-sm">
        <summary className="cursor-pointer select-none text-xs uppercase tracking-wide text-zinc-500 hover:text-zinc-700 dark:text-zinc-400 dark:hover:text-zinc-200">
          {t('manualFieldsToggle')}
        </summary>
        <div className="space-y-2 pt-2">
          <CopyableField label={t('fields.controllerURL')} value={controllerURL} mono />
          <CopyableField label={t('fields.tenantSlug')} value={tenantSlug} mono />
          <CopyableField label={t('fields.preAuthKey')} value={secret} mono secret />
        </div>
      </details>

      <div className="rounded-md border border-amber-300 px-3 py-2 text-xs text-amber-700 dark:border-amber-900/50 dark:text-amber-400">
        {t('oneShotWarning')}
      </div>

      <div className="flex justify-end">
        <button
          type="button"
          onClick={onDone}
          className="rounded-md bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 dark:bg-zinc-100 dark:text-zinc-900 dark:hover:bg-zinc-300"
        >
          {t('done')}
        </button>
      </div>
    </div>
  );
}

function CopyableField({
  label,
  value,
  mono,
  secret,
}: {
  label: string;
  value: string;
  mono?: boolean;
  secret?: boolean;
}) {
  const t = useTranslations('peers.newPeer');
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API can fail in non-secure contexts; the value
      // is still visible for manual copy, no need to alert.
    }
  }

  return (
    <div className="space-y-1">
      <label className="text-xs uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
        {label}
      </label>
      <div className="flex gap-2">
        <input
          type="text"
          value={value}
          readOnly
          className={`flex-1 select-all rounded border border-zinc-300 bg-zinc-50 px-2 py-1.5 text-xs text-zinc-900 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100 ${
            mono ? 'font-mono' : ''
          } ${secret ? 'tracking-tight' : ''}`}
          onFocus={(e) => e.target.select()}
        />
        <button
          type="button"
          onClick={copy}
          className="rounded border border-zinc-300 px-2 text-xs text-zinc-700 hover:bg-zinc-50 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-800"
        >
          {copied ? t('copied') : t('copy')}
        </button>
      </div>
    </div>
  );
}
