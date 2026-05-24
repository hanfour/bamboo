// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';
import { mintAPITokenAction, revokeAPITokenAction } from '@/lib/actions';
import type { APIToken } from '@/lib/types';

// APITokensView is the §4 P2 API-tokens admin surface. Layout
// mirrors WebhooksView:
//
//   ┌────────────────────────────────────────────────────────┐
//   │ Mint API token (button → opens inline form)            │
//   ├────────────────────────────────────────────────────────┤
//   │ Existing tokens                                        │
//   │   row: name + status + lastUsed + Revoke button        │
//   └────────────────────────────────────────────────────────┘
//
// Status derivation matches the controller's APIToken.IsActive
// semantics so a metric anomaly is debuggable against the same UI:
//   * revokedAt set                 → "revoked"
//   * expiresAt set + past now      → "expired"
//   * lastUsedAt set                → "active"
//   * else                          → "pending" (never used yet)
export function APITokensView({ tokens }: { tokens: APIToken[] }) {
  const t = useTranslations('apiTokens');
  const [error, setError] = useState<string | null>(null);
  const [minted, setMinted] = useState<MintedToken | null>(null);
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
        <MintForm
          onError={setError}
          onMinted={(m) => {
            setFormOpen(false);
            setMinted(m);
            setError(null);
          }}
        />
      )}

      {tokens.length === 0 ? (
        <p className="rounded-md border border-dashed border-ink-700 p-6 text-center text-sm text-bamboo-200/60">
          {t('empty')}
        </p>
      ) : (
        <ol className="divide-y divide-ink-800 rounded-lg border border-ink-800">
          {tokens.map((tk) => (
            <TokenRow key={tk.id} token={tk} onError={setError} />
          ))}
        </ol>
      )}

      {minted && <TokenRevealModal minted={minted} onClose={() => setMinted(null)} />}
    </div>
  );
}

type MintedToken = { id: string; name: string; token: string };

// MintForm is the inline new-token form. Name is required;
// description and expiresAt are optional. Date input is type=date
// for "expires on day X" UX; the controller accepts any RFC3339
// timestamp so a v2 datetime input would slot in cleanly.
function MintForm({
  onError,
  onMinted,
}: {
  onError: (msg: string | null) => void;
  onMinted: (m: MintedToken) => void;
}) {
  const t = useTranslations('apiTokens.form');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [expiresOn, setExpiresOn] = useState('');
  const [pending, startTransition] = useTransition();

  function submit() {
    onError(null);
    startTransition(async () => {
      // date input gives YYYY-MM-DD; we send it as end-of-day UTC
      // so a token created "expires 2026-12-31" actually lasts
      // through 2026-12-31 in every timezone west of UTC.
      let iso: string | undefined;
      if (expiresOn) {
        iso = `${expiresOn}T23:59:59Z`;
      }
      const res = await mintAPITokenAction({ name, description, expiresAt: iso });
      if (res.ok) {
        onMinted({ id: res.id, name: name.trim(), token: res.token });
        setName('');
        setDescription('');
        setExpiresOn('');
      } else {
        onError(res.error);
      }
    });
  }

  return (
    <div className="space-y-3 rounded-md border border-ink-800 p-4">
      <FormField label={t('nameLabel')} hint={t('nameHint')}>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t('namePlaceholder')}
          disabled={pending}
          className="w-full rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1.5 text-sm text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
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
      <FormField label={t('expiresLabel')} hint={t('expiresHint')}>
        <input
          type="date"
          value={expiresOn}
          onChange={(e) => setExpiresOn(e.target.value)}
          disabled={pending}
          className="rounded border border-bamboo-200/30 bg-ink-950 px-2 py-1.5 text-sm text-bamboo-50 outline-none focus:border-bamboo-300 focus:ring-1 focus:ring-bamboo-300"
        />
      </FormField>
      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={submit}
          disabled={pending || name.trim() === ''}
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

type TokenStatus = 'active' | 'pending' | 'expired' | 'revoked';

// deriveStatus mirrors APIToken.IsActive on the controller side
// (see internal/db/repo/api_tokens.go). Order matters: revoked
// dominates expired (a revoked-then-expired token reads as
// revoked, which is what the audit log says).
function deriveStatus(t: APIToken): TokenStatus {
  if (t.revokedAt) return 'revoked';
  if (t.expiresAt && new Date(t.expiresAt) < new Date()) return 'expired';
  if (t.lastUsedAt) return 'active';
  return 'pending';
}

// TokenRow renders one row with status badge + last-used summary +
// inline Revoke (with two-click confirm, same pattern as
// WebhooksView).
function TokenRow({ token, onError }: { token: APIToken; onError: (msg: string | null) => void }) {
  const t = useTranslations('apiTokens');
  const [pending, startTransition] = useTransition();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const status = deriveStatus(token);

  function doRevoke() {
    onError(null);
    startTransition(async () => {
      const res = await revokeAPITokenAction(token.id);
      if (!res.ok) onError(res.error);
    });
  }

  return (
    <li className="space-y-2 p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex items-baseline gap-2">
            <span className="font-medium text-bamboo-50">{token.name}</span>
            <StatusBadge status={status} />
          </div>
          {token.description && (
            <p className="text-xs text-bamboo-200/70">{token.description}</p>
          )}
          <p className="text-xs text-bamboo-200/50">
            {t('row.created', { when: formatRelative(token.createdAt) })}
            {token.lastUsedAt && ` · ${t('row.lastUsed', { when: formatRelative(token.lastUsedAt) })}`}
            {token.expiresAt && ` · ${t('row.expires', { when: formatRelative(token.expiresAt) })}`}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {status !== 'revoked' &&
            (confirmDelete ? (
              <>
                <button
                  type="button"
                  onClick={doRevoke}
                  disabled={pending}
                  className="rounded border border-red-900/50 bg-red-900/20 px-2.5 py-1 text-xs font-medium text-red-300 transition-colors hover:bg-red-900/30 disabled:opacity-50"
                >
                  {t('actions.confirmRevoke')}
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
                {t('actions.revoke')}
              </button>
            ))}
        </div>
      </div>
    </li>
  );
}

function StatusBadge({ status }: { status: TokenStatus }) {
  const t = useTranslations('apiTokens.status');
  const styles: Record<TokenStatus, string> = {
    active:
      'border-emerald-700/40 bg-emerald-900/20 text-emerald-300',
    pending: 'border-bamboo-200/30 text-bamboo-200/60',
    expired: 'border-amber-700/40 bg-amber-900/20 text-amber-300',
    revoked: 'border-ink-700 bg-ink-800 text-bamboo-200/60',
  };
  return (
    <span
      className={`rounded border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide ${styles[status]}`}
    >
      {t(status)}
    </span>
  );
}

// formatRelative renders an ISO timestamp as a short
// human-readable relative span. Anything older than a day falls
// back to a yyyy-mm-dd date so the row stays sortable by eye.
function formatRelative(iso: string): string {
  const then = new Date(iso);
  const now = new Date();
  const sec = Math.floor((now.getTime() - then.getTime()) / 1000);
  if (Number.isNaN(sec)) return iso;
  if (sec < 0) {
    // Future timestamp (typically expiresAt).
    const future = -sec;
    if (future < 86_400) return `in ${Math.ceil(future / 3600)}h`;
    return `in ${Math.ceil(future / 86_400)}d`;
  }
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86_400) return `${Math.floor(sec / 3600)}h ago`;
  if (sec < 604_800) return `${Math.floor(sec / 86_400)}d ago`;
  return then.toISOString().slice(0, 10);
}

// TokenRevealModal shows the freshly-minted plaintext once.
// Operator must copy it before clicking Done — no recovery path
// on the controller side. Modal stays open until explicit
// dismissal so a background click doesn't lose the secret
// (same convention as WebhooksView's secret modal).
function TokenRevealModal({ minted, onClose }: { minted: MintedToken; onClose: () => void }) {
  const t = useTranslations('apiTokens.secretModal');
  const [copied, setCopied] = useState(false);

  function copy() {
    void navigator.clipboard.writeText(minted.token).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="api-token-secret-title"
      className="fixed inset-0 z-50 flex items-center justify-center bg-ink-950/80 p-4"
    >
      <div className="w-full max-w-lg space-y-4 rounded-lg border border-bamboo-200/30 bg-ink-950 p-6">
        <h2 id="api-token-secret-title" className="text-lg font-medium text-bamboo-50">
          {t('title')}
        </h2>
        <p className="text-sm text-bamboo-200/70">{t('description')}</p>
        <div className="space-y-1">
          <label className="block text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
            {t('nameLabel')}
          </label>
          <p className="text-sm text-bamboo-100">{minted.name}</p>
        </div>
        <div className="space-y-1">
          <label className="block text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
            {t('tokenLabel')}
          </label>
          <code className="block break-all rounded border border-amber-900/50 bg-amber-900/20 px-2 py-1.5 font-mono text-xs text-amber-100">
            {minted.token}
          </code>
          <p className="text-xs text-amber-300/80">{t('warning')}</p>
        </div>
        <div className="rounded border border-ink-800 bg-ink-900/40 p-3 text-xs text-bamboo-200/70">
          <p className="mb-1 font-medium text-bamboo-100">{t('usageLabel')}</p>
          <code className="block break-all font-mono text-[11px] text-bamboo-100">
            curl -H &quot;Authorization: Bearer {minted.token}&quot; …
          </code>
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
