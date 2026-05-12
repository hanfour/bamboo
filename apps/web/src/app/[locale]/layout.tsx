// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { Saira_Stencil_One } from 'next/font/google';
import { routing } from '@/i18n/routing';
import { TopBar } from '@/components/TopBar';
import { Sidebar } from '@/components/Sidebar';
import { ChromeShell } from '@/components/ChromeShell';
import '../globals.css';

// Saira Stencil One — loaded only for the wordmark via the
// --font-wordmark CSS variable. The rest of the site keeps the
// system-sans stack defined in globals.css; pulling a display
// font onto body text would push us back toward the editorial
// look that got rejected on 2026-05-12.
const wordmarkFont = Saira_Stencil_One({
  subsets: ['latin'],
  weight: '400',
  variable: '--font-wordmark',
  display: 'swap',
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
    <html lang={locale} className={wordmarkFont.variable}>
      <body className="min-h-screen antialiased">
        <NextIntlClientProvider messages={messages}>
          {/* Google-Account-style chrome: thin top bar + persistent
              left sidebar on lg+, slide-in drawer below lg. The
              ChromeShell client wrapper holds the drawer open state
              so the HamburgerButton in TopBar can toggle the Sidebar
              even though they're separate component subtrees. */}
          <ChromeShell topBar={<TopBar />} sidebar={<Sidebar />}>
            {children}
          </ChromeShell>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}
