// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';

type SearchParams = { token?: string };

// Invite landing page — the URL admins share with invitees:
//
//   bamboo.miilink.net/{locale}/invite?token=<bki_token>
//
// We do NOT validate the token on the Web side because:
//   1. The controller will validate on the actual OIDC redirect, and
//      revealing "this token is valid for alice@example.com" before
//      OAuth would let a guesser probe live invitations.
//   2. Validation requires an admin-only REST call we don't have here.
//
// So we render a welcoming card with a single CTA — "Continue with
// Google to accept" — that links to
// /auth/google/login?invite=<token>. The controller's handleLogin
// validates the token, embeds the invite id in OIDC state, and
// redirects to Google. Any token problems (expired, revoked, malformed)
// surface as a plain-text 4xx page after the click.
//
// Missing ?token at all is rendered as an inline error card so we
// don't punish a bare /invite visit with a confusing OAuth redirect.
const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';

export default async function InvitePage({
  searchParams,
}: {
  searchParams: Promise<SearchParams>;
}) {
  const { token } = await searchParams;
  if (!token) {
    return <MissingTokenCard />;
  }
  return <InviteCard token={token} />;
}

function InviteCard({ token }: { token: string }) {
  const t = useTranslations('invite');
  const loginUrl = `${CONTROLLER_BASE}/auth/google/login?invite=${encodeURIComponent(token)}`;
  return (
    <div className="mx-auto max-w-xl space-y-6">
      <header className="space-y-2">
        <h1 className="text-3xl font-semibold tracking-tight">{t('title')}</h1>
        <p className="text-sm leading-relaxed text-zinc-600 dark:text-zinc-400">
          {t('subtitle')}
        </p>
      </header>

      <div className="rounded-lg border border-zinc-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-950">
        <a
          href={loginUrl}
          className="inline-flex w-full items-center justify-center rounded-md bg-zinc-900 px-4 py-2.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 dark:bg-zinc-100 dark:text-zinc-900 dark:hover:bg-zinc-300"
        >
          {t('signInWithGoogle')}
        </a>
      </div>

      <ul className="space-y-2 text-xs text-zinc-500 dark:text-zinc-400">
        <li>• {t('emailHint')}</li>
        <li>• {t('expiryHint')}</li>
      </ul>
    </div>
  );
}

function MissingTokenCard() {
  const t = useTranslations('invite.tokenMissing');
  return (
    <div className="mx-auto max-w-xl">
      <div className="rounded-lg border border-zinc-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-950">
        <div className="flex items-start gap-3">
          <span
            aria-hidden
            className="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center text-zinc-500 dark:text-zinc-400"
          >
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
          </span>
          <div className="min-w-0">
            <h2 className="text-base font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">
              {t('title')}
            </h2>
            <p className="mt-1 text-sm text-zinc-600 dark:text-zinc-400">{t('body')}</p>
          </div>
        </div>
      </div>
    </div>
  );
}
