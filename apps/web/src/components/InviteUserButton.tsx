// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useRef, useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';

import { inviteUserAction } from '@/lib/actions';
import { useDialogA11y } from '@/hooks/useDialogA11y';

// InviteUserButton wraps the /users header's "Invite user" button
// with a modal that posts to /api/v1/invitations and shows the
// plaintext token once in a result view. Mirrors NewPeerButton's
// mint flow — same useDialogA11y, same FormView → ResultView state
// machine, same deferred reset on close.
//
// Currently the redeem (accept-on-OIDC-callback) path is not wired
// in the controller, so the result modal carries a prominent honest
// "don't share this yet" warning. The accept-flow PR will remove
// that warning + drop in a real share affordance.
export function InviteUserButton() {
  const t = useTranslations('users.newInvite');
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState('');
  const [isAdmin, setIsAdmin] = useState(false);
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<{ msg: string; duplicate: boolean } | null>(null);
  const [result, setResult] = useState<{
    id: string;
    email: string;
    token: string;
    expiresAt: string;
    emailSent: boolean;
  } | null>(null);

  function close() {
    setOpen(false);
    // Defer reset so the modal can fade out before content swaps.
    // Same idiom as NewPeerButton.
    setTimeout(() => {
      setEmail('');
      setIsAdmin(false);
      setError(null);
      setResult(null);
    }, 200);
  }

  function submit() {
    setError(null);
    startTransition(async () => {
      const res = await inviteUserAction({ email, isAdmin });
      if (res.ok) {
        setResult({
          id: res.id,
          email: res.email,
          token: res.token,
          expiresAt: res.expiresAt,
          emailSent: res.emailSent,
        });
      } else {
        setError({ msg: res.error, duplicate: Boolean(res.duplicate) });
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
              email={result.email}
              token={result.token}
              expiresAt={result.expiresAt}
              emailSent={result.emailSent}
              onDone={close}
            />
          ) : (
            <FormView
              email={email}
              isAdmin={isAdmin}
              pending={pending}
              error={error}
              onEmailChange={setEmail}
              onIsAdminChange={setIsAdmin}
              onSubmit={submit}
              onCancel={close}
            />
          )}
        </Modal>
      )}
    </>
  );
}

// Modal mirrors NewPeerButton's inline primitive. Kept inline rather
// than promoted to a shared component until a third surface needs it.
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
  useDialogA11y({ open: true, onClose, panelRef, trapTab: true });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-zinc-900/40 dark:bg-black/60" onClick={onClose} />
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
  email,
  isAdmin,
  pending,
  error,
  onEmailChange,
  onIsAdminChange,
  onSubmit,
  onCancel,
}: {
  email: string;
  isAdmin: boolean;
  pending: boolean;
  error: { msg: string; duplicate: boolean } | null;
  onEmailChange: (v: string) => void;
  onIsAdminChange: (v: boolean) => void;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  const t = useTranslations('users.newInvite');
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
        <span className="text-zinc-700 dark:text-zinc-300">{t('emailLabel')}</span>
        <input
          type="email"
          required
          autoFocus
          value={email}
          disabled={pending}
          onChange={(e) => onEmailChange(e.target.value)}
          placeholder={t('emailPlaceholder')}
          className="w-full rounded border border-zinc-300 bg-white px-2 py-1.5 text-sm text-zinc-900 outline-none focus:border-bamboo-500 focus:ring-1 focus:ring-bamboo-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
        />
      </label>

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={isAdmin}
          disabled={pending}
          onChange={(e) => onIsAdminChange(e.target.checked)}
        />
        <span className="text-zinc-700 dark:text-zinc-300">{t('isAdminLabel')}</span>
      </label>
      <p className="-mt-2 text-xs text-zinc-500 dark:text-zinc-400">{t('isAdminHint')}</p>

      {error && (
        <div className="rounded-md border border-red-300 px-3 py-2 text-sm text-red-700 dark:border-red-900/50 dark:text-red-400">
          {error.duplicate ? t('errorDuplicate') : `${t('error')} ${error.msg}`}
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
          disabled={pending || email.trim() === ''}
          className="rounded-md bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900 dark:hover:bg-zinc-300"
        >
          {pending ? t('working') : t('submit')}
        </button>
      </div>
    </form>
  );
}

function ResultView({
  email,
  token,
  expiresAt,
  emailSent,
  onDone,
}: {
  email: string;
  token: string;
  expiresAt: string;
  emailSent: boolean;
  onDone: () => void;
}) {
  const t = useTranslations('users.newInvite');
  // Top hint flips on whether the controller's SMTP relay accepted
  // the invite email. When sent, the token below is a backup the
  // admin can still copy if email gets stuck in spam. When not sent
  // (SMTP unconfigured or relay failure), we surface that honestly
  // so the admin knows they need to share the token manually.
  return (
    <div className="space-y-4">
      <p className="text-sm text-zinc-700 dark:text-zinc-300">
        {emailSent ? t('resultHintEmailed', { email }) : t('resultHint')}
      </p>
      {!emailSent && (
        <p className="text-xs text-zinc-500 dark:text-zinc-400">{t('emailNotConfigured')}</p>
      )}
      <p className="text-xs text-zinc-500 dark:text-zinc-400">
        {t('emailLabel')}: <span className="font-medium">{email}</span>
      </p>

      <CopyableField label={t('tokenLabel')} value={token} mono secret />

      <p className="font-mono text-[11px] text-zinc-400 dark:text-zinc-500">{expiresAt}</p>

      {/* Redeem-flow warning is now informational rather than a
          blocker — accept-flow shipped in #85. Kept the copy until
          we audit that staging confirms end-to-end works. Future
          PR removes the warning entirely. */}
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
  const t = useTranslations('users.newInvite');
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API can fail in non-secure contexts; the value is
      // still visible for manual copy.
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
          className="rounded border border-zinc-300 px-2 text-xs text-zinc-700 transition-colors hover:border-zinc-400 hover:text-zinc-900 dark:border-zinc-700 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:text-zinc-100"
        >
          {copied ? t('copied') : t('copy')}
        </button>
      </div>
    </div>
  );
}
