// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Shared page header for the four logged-in pages. Three slots:
//   - kicker: small uppercase mono superlabel ("OVERVIEW", "PEERS", etc.)
//   - title: editorial serif h1, the page's name
//   - meta: optional right-aligned content (revision number, action button)
// Plus a hairline rule that sits below the title row and visually
// closes the header against the page body. This is the unifying
// element that gives every page a consistent "field journal" feel.

import type { ReactNode } from 'react';

export function PageHeader({
  kicker,
  title,
  description,
  meta,
}: {
  kicker: string;
  title: string;
  description?: ReactNode;
  meta?: ReactNode;
}) {
  return (
    <header className="mb-10">
      <div className="flex flex-wrap items-end justify-between gap-x-10 gap-y-4">
        <div className="min-w-0">
          <p className="font-mono text-[11px] uppercase tracking-[0.24em] text-bamboo-400/90">
            <span className="mr-2 inline-block h-px w-6 align-middle bg-bamboo-400/60" />
            {kicker}
          </p>
          <h1 className="mt-3 font-serif text-4xl font-light leading-[1.05] tracking-display text-zinc-50 sm:text-5xl">
            {title}
          </h1>
          {description ? (
            <p className="mt-3 max-w-2xl text-sm leading-relaxed text-zinc-400 sm:text-base">
              {description}
            </p>
          ) : null}
        </div>
        {meta ? (
          <div className="flex shrink-0 items-center gap-3">{meta}</div>
        ) : null}
      </div>
      <div className="mt-7 h-px bg-gradient-to-r from-bamboo-400/40 via-white/[0.08] to-transparent" />
    </header>
  );
}
