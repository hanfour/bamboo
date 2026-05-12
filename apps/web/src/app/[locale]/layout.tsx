// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from 'react';
import { DM_Mono, DM_Sans, Newsreader } from 'next/font/google';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { routing } from '@/i18n/routing';
import { Header } from '@/components/Header';
import '../globals.css';

// Three families, each loaded from Google Fonts via next/font so they
// ship as self-hosted CSS at build time (no runtime CDN dependency).
//
// - DM Sans: body UI. Humanist sans with low contrast — calm on dense
//   tables.
// - DM Mono: numbers, code, kicker labels. Pairs with DM Sans (same
//   designer) so the metrics + IP cells visually belong to the
//   surrounding sentence.
// - Newsreader: editorial display. Variable serif with a real optical-
//   size axis; the page hero uses the large optical cut and the body
//   uses the smaller. The italic is used for the BAMBOO wordmark.
const dmSans = DM_Sans({
  subsets: ['latin'],
  variable: '--font-sans',
  display: 'swap',
});

const dmMono = DM_Mono({
  subsets: ['latin'],
  weight: ['400', '500'],
  variable: '--font-mono',
  display: 'swap',
});

const newsreader = Newsreader({
  subsets: ['latin'],
  variable: '--font-serif',
  display: 'swap',
  // Italic carries the wordmark — load it explicitly.
  style: ['normal', 'italic'],
  // Newsreader doesn't ship with the metric overrides Next uses for
  // automatic CLS-safe fallbacks; opt out rather than fail the build
  // on a noisy warning. The serif fallback in globals.css covers the
  // pre-swap layout adequately.
  adjustFontFallback: false,
});

export const metadata = {
  title: 'bamboo',
  description: 'AI-native zero-trust mesh networking',
};

type Params = { locale: string };

export default async function LocaleLayout({
  children,
  params,
}: {
  children: ReactNode;
  params: Promise<Params>;
}) {
  const { locale } = await params;
  if (!routing.locales.includes(locale as never)) {
    notFound();
  }
  const messages = await getMessages();

  return (
    <html
      lang={locale}
      className={`${dmSans.variable} ${dmMono.variable} ${newsreader.variable}`}
    >
      <body className="min-h-screen font-sans antialiased">
        <NextIntlClientProvider messages={messages}>
          <Header />
          <main className="mx-auto max-w-6xl px-6 pb-24 pt-8 sm:px-10">
            {children}
          </main>
          <SiteFooter />
        </NextIntlClientProvider>
      </body>
    </html>
  );
}

// Quiet bottom rule. Anchors the page composition and gives every
// screen a "you reached the end" signal in long-table pages.
function SiteFooter() {
  return (
    <footer className="mx-auto max-w-6xl px-6 pb-10 pt-6 sm:px-10">
      <div className="flex items-baseline justify-between border-t border-white/[0.06] pt-4 text-[10px] uppercase tracking-[0.18em] text-zinc-500">
        <span className="font-mono">bamboo · zero-trust mesh</span>
        <span className="font-mono opacity-60">
          v0.1.7
        </span>
      </div>
    </footer>
  );
}
