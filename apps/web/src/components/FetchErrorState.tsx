// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Shared renderer for non-ok FetchResult variants.
//
// Of the four variants, "unauthorized" is the de-facto landing page
// for the entire product (when require_auth is on, every protected
// page route lands here until the user signs in). So we give it the
// hero treatment: a large editorial wordmark, a kicker, the tagline,
// and clearly-differentiated sign-in chips for each provider. The
// other variants keep the same visual vocabulary but at compact scale
// — they're recoverable interruptions, not the primary entry point.

import { useTranslations } from 'next-intl';

import { Wordmark } from '@/components/Wordmark';
import type { FetchResult } from '@/lib/types';

const BASE = process.env.NEXT_PUBLIC_BAMBOO_BASE_URL ?? '';
const TENANT = process.env.NEXT_PUBLIC_BAMBOO_TENANT ?? 'default';

type Variant = Exclude<FetchResult<unknown>['kind'], 'ok'>;

export function FetchErrorState({
  kind,
  message,
}: {
  kind: Variant;
  message?: string;
  /** @deprecated retained for backwards compatibility; ignored. */
  className?: string;
}) {
  if (kind === 'unauthorized') {
    return <UnauthorizedHero />;
  }
  return <CompactNotice kind={kind} message={message} />;
}

// The unauthenticated landing. Composition is split into a hero block
// (large wordmark + tagline) and a recessed sign-in card with the two
// provider chips. The hairline grid backdrop and a single bamboo-tinted
// glow at the upper-left lift the composition off the page without
// resorting to gradient noise.
function UnauthorizedHero() {
  const t = useTranslations('fetchError');
  const tLanding = useTranslations('landing');

  return (
    <section
      aria-labelledby="signin-heading"
      className="bamboo-grid relative -mx-6 -mt-8 min-h-[78vh] overflow-hidden px-6 pt-12 sm:-mx-10 sm:px-10 sm:pt-20"
    >
      {/* atmospheric bamboo accent glow, anchored top-left */}
      <div
        aria-hidden
        className="pointer-events-none absolute -left-32 -top-32 h-[420px] w-[420px] rounded-full bg-bamboo-500/10 blur-3xl"
      />

      <div className="relative mx-auto flex max-w-3xl flex-col items-start gap-12 py-6 sm:py-10">
        <header className="flex flex-col gap-6 [animation-delay:60ms] motion-safe:animate-fade-up">
          <span className="inline-flex items-center gap-3 font-mono text-[11px] uppercase tracking-[0.24em] text-bamboo-400/90">
            <span className="h-px w-8 bg-bamboo-400/60" />
            {tLanding('subtitle')}
          </span>
          <Wordmark size="lg" href={null} />
          <p className="max-w-xl text-base leading-relaxed text-zinc-400 sm:text-lg">
            {tLanding('selfHosted')}
          </p>
        </header>

        <article
          id="signin-heading"
          className="relative w-full max-w-xl rounded-lg border border-white/[0.08] bg-ink-900/60 p-7 shadow-elevated backdrop-blur-sm [animation-delay:200ms] motion-safe:animate-fade-up sm:p-9"
        >
          <span
            aria-hidden
            className="absolute -top-px left-7 h-px w-12 bg-bamboo-400"
          />
          <h2 className="font-mono text-[11px] uppercase tracking-[0.24em] text-zinc-500">
            authentication required
          </h2>
          <p className="mt-3 font-serif text-2xl font-light leading-snug tracking-display text-zinc-100">
            {t('unauthorized.title')}
          </p>
          <p className="mt-3 max-w-md text-sm leading-relaxed text-zinc-400">
            {t('unauthorized.body')}
          </p>

          <SignInChips
            base={BASE}
            tenantSlug={TENANT}
            label={t('unauthorized.signIn')}
          />

          <div className="mt-7 flex items-center gap-3 text-[10px] uppercase tracking-[0.22em] text-zinc-600">
            <span className="h-px flex-1 bg-white/[0.06]" />
            <span className="font-mono">tenant · {TENANT}</span>
            <span className="h-px flex-1 bg-white/[0.06]" />
          </div>
        </article>

        <ProductFooter />
      </div>
    </section>
  );
}

function ProductFooter() {
  const tLanding = useTranslations('landing');
  return (
    <p className="font-mono text-[11px] uppercase tracking-[0.20em] text-zinc-600 [animation-delay:360ms] motion-safe:animate-fade-up">
      {tLanding('title')} ·{' '}
      <span className="text-zinc-500">
        {tLanding('signInWithGoogle')}
      </span>
    </p>
  );
}

function SignInChips({
  base,
  tenantSlug,
  label,
}: {
  base: string;
  tenantSlug: string;
  label: string;
}) {
  const url = (provider: string) =>
    `${base}/auth/${provider}/login?tenant=${encodeURIComponent(tenantSlug)}`;

  return (
    <div className="mt-6 grid gap-2 sm:grid-cols-2">
      <ProviderChip href={url('google')} provider="google" label={label} />
      <ProviderChip href={url('github')} provider="github" label={label} />
    </div>
  );
}

function ProviderChip({
  href,
  provider,
  label,
}: {
  href: string;
  provider: 'google' | 'github';
  label: string;
}) {
  const display = provider === 'google' ? 'Google' : 'GitHub';
  return (
    <a
      href={href}
      className="group relative flex items-center justify-between rounded-md border border-white/[0.08] bg-white/[0.02] px-4 py-3 transition hover:border-bamboo-400/40 hover:bg-white/[0.04]"
    >
      <span className="flex items-center gap-3">
        <ProviderGlyph provider={provider} />
        <span className="flex flex-col leading-tight">
          <span className="font-mono text-[10px] uppercase tracking-[0.22em] text-zinc-500 group-hover:text-zinc-400">
            {label}
          </span>
          <span className="text-sm font-medium text-zinc-100">{display}</span>
        </span>
      </span>
      <Arrow />
    </a>
  );
}

function ProviderGlyph({ provider }: { provider: 'google' | 'github' }) {
  if (provider === 'google') {
    return (
      <svg
        viewBox="0 0 24 24"
        className="h-5 w-5 text-bamboo-400"
        aria-hidden
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <circle cx="12" cy="12" r="8.5" />
        <path d="M12 7.5v4.5h4.5" />
      </svg>
    );
  }
  // github — abstract: a single octagonal-ish path that doesn't try
  // to be the github logo (we don't want trademark issues) but reads
  // as "alternate provider"
  return (
    <svg
      viewBox="0 0 24 24"
      className="h-5 w-5 text-bamboo-400"
      aria-hidden
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M12 3.5l7.5 3v6c0 4.4-3 7.5-7.5 8-4.5-.5-7.5-3.6-7.5-8v-6L12 3.5z" />
      <path d="M9 12.5l2 2 4-4.5" />
    </svg>
  );
}

function Arrow() {
  return (
    <svg
      viewBox="0 0 24 24"
      className="h-4 w-4 text-zinc-600 transition-transform group-hover:translate-x-0.5 group-hover:text-bamboo-400"
      aria-hidden
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M5 12h14M13 6l6 6-6 6" />
    </svg>
  );
}

// Compact treatment used by forbidden / notFound / error. Same
// language as the hero but at scope-of-page-section instead of hero.
function CompactNotice({ kind, message }: { kind: Variant; message?: string }) {
  const t = useTranslations('fetchError');
  const map: Record<
    Exclude<Variant, 'unauthorized'>,
    {
      kicker: string;
      title: string;
      body: string;
      accent: string;
      bg: string;
    }
  > = {
    forbidden: {
      kicker: 'permission',
      title: t('forbidden.title'),
      body: t('forbidden.body'),
      accent: 'border-amber-400/40 text-amber-300',
      bg: 'bg-amber-500/[0.04]',
    },
    notFound: {
      kicker: 'not found',
      title: t('notFound.title'),
      body: t('notFound.body'),
      accent: 'border-white/[0.08] text-zinc-400',
      bg: 'bg-white/[0.02]',
    },
    error: {
      kicker: 'controller unreachable',
      title: t('unreachable.title'),
      body: t('unreachable.body'),
      accent: 'border-red-400/40 text-red-300',
      bg: 'bg-red-500/[0.04]',
    },
  };
  const cfg = map[kind as Exclude<Variant, 'unauthorized'>];

  return (
    <div
      role="alert"
      className={`relative overflow-hidden rounded-lg border ${cfg.accent.split(' ')[0]} ${cfg.bg} px-6 py-7`}
    >
      <span
        aria-hidden
        className={`absolute -top-px left-6 h-px w-12 ${
          kind === 'error'
            ? 'bg-red-400/70'
            : kind === 'forbidden'
              ? 'bg-amber-400/70'
              : 'bg-zinc-500/70'
        }`}
      />
      <p
        className={`font-mono text-[11px] uppercase tracking-[0.24em] ${cfg.accent.split(' ')[1]}`}
      >
        {cfg.kicker}
      </p>
      <h2 className="mt-2 font-serif text-xl font-light tracking-display text-zinc-100">
        {cfg.title}
      </h2>
      <p className="mt-2 max-w-2xl text-sm leading-relaxed text-zinc-400">
        {cfg.body}
      </p>
      {kind === 'error' ? (
        <a
          href=""
          className="mt-5 inline-flex items-center gap-2 rounded-md border border-red-400/30 bg-red-500/[0.06] px-3 py-1.5 font-mono text-[11px] uppercase tracking-[0.18em] text-red-300 transition hover:bg-red-500/[0.10]"
        >
          {t('unreachable.retry')}
          <svg viewBox="0 0 24 24" className="h-3.5 w-3.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
            <path d="M3 12a9 9 0 1 0 3-6.7M3 4v5h5" />
          </svg>
        </a>
      ) : null}
      {message ? (
        <p className="mt-3 font-mono text-[11px] text-zinc-500">{message}</p>
      ) : null}
    </div>
  );
}
