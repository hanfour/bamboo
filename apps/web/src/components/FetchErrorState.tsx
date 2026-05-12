// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Shared renderer for non-ok FetchResult variants. Pages that fetch
// data on the server route their failed reads through this so the UI
// vocabulary for auth + network errors is uniform across /, /peers,
// /preauth-keys, and /acl.
//
// Why not auto-redirect on 401: a redirect loop when OIDC isn't
// configured (e.g. self-hosted operator hasn't set OIDC env yet) is
// worse than rendering an explicit "Sign in" affordance the user can
// reach when they're ready. The link points at the controller's
// /auth/{provider}/login flow; the provider is operator-chosen so we
// surface both Google and GitHub and let the buttons 404 cleanly if
// the provider isn't configured.

import { useTranslations } from 'next-intl';

import type { FetchResult } from '@/lib/types';

const BASE = process.env.NEXT_PUBLIC_BAMBOO_BASE_URL ?? '';
const TENANT = process.env.NEXT_PUBLIC_BAMBOO_TENANT ?? 'default';

type Variant = Exclude<FetchResult<unknown>['kind'], 'ok'>;

// Render is for callers that want to short-circuit the page when the
// result isn't ok. Usage:
//   const r = await fetchPeers();
//   if (r.kind !== 'ok') return <FetchErrorState kind={r.kind} />;
//   ...render r.value
export function FetchErrorState({
  kind,
  message,
  className = '',
}: {
  kind: Variant;
  message?: string;
  className?: string;
}) {
  const t = useTranslations('fetchError');

  switch (kind) {
    case 'unauthorized':
      return (
        <ErrorCard
          className={className}
          tone="info"
          title={t('unauthorized.title')}
          body={t('unauthorized.body')}
        >
          <SignInLinks tenantSlug={TENANT} base={BASE} label={t('unauthorized.signIn')} />
        </ErrorCard>
      );
    case 'forbidden':
      return (
        <ErrorCard
          className={className}
          tone="warn"
          title={t('forbidden.title')}
          body={t('forbidden.body')}
        />
      );
    case 'notFound':
      return (
        <ErrorCard
          className={className}
          tone="neutral"
          title={t('notFound.title')}
          body={t('notFound.body')}
        />
      );
    case 'error':
    default:
      return (
        <ErrorCard
          className={className}
          tone="danger"
          title={t('unreachable.title')}
          body={t('unreachable.body')}
          // The retry button is a plain <a> that re-requests the same
          // URL — server components will re-execute the fetcher and
          // either succeed or render this state again with fresh data.
          // Using location.reload would lose any router-state the page
          // is holding (e.g. ?selected= for the peer drawer).
        >
          <RetryButton label={t('unreachable.retry')} />
          {message ? (
            <p className="mt-2 text-xs font-mono text-zinc-500 dark:text-zinc-500">
              {message}
            </p>
          ) : null}
        </ErrorCard>
      );
  }
}

function ErrorCard({
  tone,
  title,
  body,
  children,
  className,
}: {
  tone: 'info' | 'warn' | 'danger' | 'neutral';
  title: string;
  body: string;
  children?: React.ReactNode;
  className?: string;
}) {
  const palette = {
    info: 'border-blue-300 bg-blue-50 text-blue-900 dark:border-blue-900/40 dark:bg-blue-950/30 dark:text-blue-100',
    warn: 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-900/40 dark:bg-amber-950/30 dark:text-amber-100',
    danger:
      'border-red-300 bg-red-50 text-red-900 dark:border-red-900/40 dark:bg-red-950/30 dark:text-red-100',
    neutral:
      'border-zinc-200 bg-zinc-50 text-zinc-900 dark:border-zinc-800 dark:bg-zinc-900/40 dark:text-zinc-100',
  }[tone];

  return (
    <div className={`rounded-lg border p-6 ${palette} ${className}`}>
      <h2 className="text-base font-semibold tracking-tight">{title}</h2>
      <p className="mt-1 text-sm">{body}</p>
      {children ? <div className="mt-4">{children}</div> : null}
    </div>
  );
}

function SignInLinks({
  base,
  tenantSlug,
  label,
}: {
  base: string;
  tenantSlug: string;
  label: string;
}) {
  // base is the public controller URL — empty in dev SSR where the
  // browser will resolve against the current origin via Caddy. We
  // ship absolute URLs only when NEXT_PUBLIC_BAMBOO_BASE_URL is
  // explicitly set (production), otherwise relative paths that the
  // Web's own reverse-proxy will route to the controller.
  const url = (provider: string) =>
    `${base}/auth/${provider}/login?tenant=${encodeURIComponent(tenantSlug)}`;

  return (
    <div className="flex flex-wrap gap-2">
      <a
        href={url('google')}
        className="inline-flex items-center rounded-md border border-current/20 bg-white/60 px-3 py-1.5 text-sm font-medium hover:bg-white dark:bg-zinc-900/40 dark:hover:bg-zinc-900"
      >
        {label} — Google
      </a>
      <a
        href={url('github')}
        className="inline-flex items-center rounded-md border border-current/20 bg-white/60 px-3 py-1.5 text-sm font-medium hover:bg-white dark:bg-zinc-900/40 dark:hover:bg-zinc-900"
      >
        {label} — GitHub
      </a>
    </div>
  );
}

function RetryButton({ label }: { label: string }) {
  // We can't use client-side onClick handlers in a server component, so
  // the retry is a same-URL <a> the browser handles. The server-render
  // re-executes the fetcher.
  return (
    <a
      href=""
      className="inline-flex items-center rounded-md border border-current/30 bg-white/60 px-3 py-1.5 text-sm font-medium hover:bg-white dark:bg-zinc-900/40 dark:hover:bg-zinc-900"
    >
      {label}
    </a>
  );
}
