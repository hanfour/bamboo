// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { routing } from '@/i18n/routing';
import { Header } from '@/components/Header';
import '../globals.css';

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
    <html lang={locale}>
      <body className="min-h-screen">
        <NextIntlClientProvider messages={messages}>
          <Header />
          <main className="mx-auto max-w-6xl px-6 py-8">{children}</main>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}
