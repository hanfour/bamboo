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
// /auth/{provider}/login flow.
//
// Only Google is offered today — GitHub OIDC is unimplemented in
// production (OIDC_GITHUB_* env vars are empty on the VPS) and the
// button used to 404 on click. When GitHub auth lands, gate visible
// providers behind a /api/v1/me-style "configured providers" list so
// self-hosted operators see exactly what's wired.

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
          icon={<LockIcon />}
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
          icon={<ShieldIcon />}
          title={t('forbidden.title')}
          body={t('forbidden.body')}
        />
      );
    case 'notFound':
      return (
        <ErrorCard
          className={className}
          icon={<QuestionIcon />}
          title={t('notFound.title')}
          body={t('notFound.body')}
        />
      );
    case 'error':
    default:
      return (
        <ErrorCard
          className={className}
          icon={<CloudOffIcon />}
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
            <p className="mt-2 font-mono text-xs text-zinc-500 dark:text-zinc-500">
              {message}
            </p>
          ) : null}
        </ErrorCard>
      );
  }
}

function ErrorCard({
  icon,
  title,
  body,
  children,
  className,
}: {
  icon: React.ReactNode;
  title: string;
  body: string;
  children?: React.ReactNode;
  className?: string;
}) {
  // Single neutral card for every variant — Muji direction: no tone
  // tints. The outlined icon is the only state differentiator alongside
  // the title copy itself. Hairline border, no fill on top of the page
  // background, identical chrome to Header.
  return (
    <div
      className={`rounded-lg border border-zinc-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-950 ${className}`}
    >
      <div className="flex items-start gap-3">
        <span
          aria-hidden
          className="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center text-zinc-500 dark:text-zinc-400"
        >
          {icon}
        </span>
        <div className="min-w-0">
          <h2 className="text-base font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">
            {title}
          </h2>
          <p className="mt-1 text-sm text-zinc-600 dark:text-zinc-400">{body}</p>
          {children ? <div className="mt-4">{children}</div> : null}
        </div>
      </div>
    </div>
  );
}

const OUTLINED_BUTTON =
  'inline-flex items-center rounded-md border border-zinc-300 px-3 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:border-zinc-400 hover:text-zinc-900 dark:border-zinc-700 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:text-zinc-100';

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
      <a href={url('google')} className={OUTLINED_BUTTON}>
        {label} — Google
      </a>
    </div>
  );
}

function RetryButton({ label }: { label: string }) {
  // We can't use client-side onClick handlers in a server component, so
  // the retry is a same-URL <a> the browser handles. The server-render
  // re-executes the fetcher.
  return (
    <a href="" className={OUTLINED_BUTTON}>
      {label}
    </a>
  );
}

// Outlined icons (stroke-only, 1.5px) — single zinc tone, no fills.
// Sized via the wrapping <span>; the SVGs themselves stretch to 100%.

function LockIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-5 w-5"
    >
      <rect x="4" y="11" width="16" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" />
    </svg>
  );
}

function ShieldIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-5 w-5"
    >
      <path d="M12 3 5 6v6c0 4.5 3 8 7 9 4-1 7-4.5 7-9V6l-7-3Z" />
    </svg>
  );
}

function QuestionIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-5 w-5"
    >
      <circle cx="12" cy="12" r="9" />
      <path d="M9.5 9a2.5 2.5 0 1 1 3.5 2.3c-.7.3-1 .8-1 1.7" />
      <path d="M12 17h.01" />
    </svg>
  );
}

function CloudOffIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-5 w-5"
    >
      <path d="m3 3 18 18" />
      <path d="M9.5 6.4A5 5 0 0 1 18 9a4 4 0 0 1 2.6 7" />
      <path d="M5.7 9A4 4 0 0 0 7 17h9" />
    </svg>
  );
}
