// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { Inter } from 'next/font/google';
import { routing } from '@/i18n/routing';
import { TopBar } from '@/components/TopBar';
import { Sidebar } from '@/components/Sidebar';
import { SidebarUser } from '@/components/SidebarUser';
import { ChromeShell } from '@/components/ChromeShell';
import '../globals.css';

// Inter, the single UI sans. Weights 400 to 600 cover body, labels and
// headings. Tabular figures come from Tailwind's `tabular-nums`.
const bodySans = Inter({
 subsets: ['latin'],
 weight: ['400', '500', '600'],
 variable: '--font-sans',
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

 // Force the dark theme via the `dark` class on <html>. The
 // tailwind config has darkMode: 'class', so this opts every
 // `dark:` variant in. Light mode is deliberately off. The look
 // follows the Cursor dashboard: neutral greys, one sans, no accents.
 return (
 <html
 lang={locale}
 className={`dark ${bodySans.variable}`}
 >
 <body className="min-h-screen bg-ink-950 font-sans text-bamboo-50 antialiased">
 <NextIntlClientProvider messages={messages}>
 {/* Google-Account-style chrome: thin top bar + persistent
 left sidebar on lg+, slide-in drawer below lg. The
 ChromeShell client wrapper holds the drawer open state
 so the HamburgerButton in TopBar can toggle the Sidebar
 even though they're separate component subtrees. */}
 <ChromeShell topBar={<TopBar />} sidebar={<Sidebar footer={<SidebarUser />} />}>
 {children}
 </ChromeShell>
 </NextIntlClientProvider>
 </body>
 </html>
 );
}
