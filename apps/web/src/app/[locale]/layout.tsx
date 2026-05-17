// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { Saira_Stencil_One, Sora, Lora } from 'next/font/google';
import { routing } from '@/i18n/routing';
import { TopBar } from '@/components/TopBar';
import { Sidebar } from '@/components/Sidebar';
import { ChromeShell } from '@/components/ChromeShell';
import '../globals.css';

// Saira Stencil One — loaded only for the wordmark via the
// --font-wordmark CSS variable. Display font kept distinct from
// body text so the stencil aesthetic stays scoped to the wordmark
// (using it for body would push us back to the editorial look
// rejected on 2026-05-12).
const wordmarkFont = Saira_Stencil_One({
 subsets: ['latin'],
 weight: '400',
 variable: '--font-wordmark',
 display: 'swap',
});

// Sora — primary body / UI sans. Humanist geometric, light weights
// land cleanly on the warm-dark theme (Tailscale-marketing ×
// Linear-app feel). 200-700 covers labels (300), body (400), and
// emphasis (500-600). Tabular-nums comes free via Tailwind's
// `tabular-nums` utility.
const bodySans = Sora({
 subsets: ['latin'],
 weight: ['200', '300', '400', '500', '600', '700'],
 variable: '--font-sans',
 display: 'swap',
});

// Lora — serif used exclusively for italic emphasis on key
// nouns/verbs in headings (a la Resend's `developers`). Body text
// stays in Sora; pulling Lora onto paragraphs would tip the page
// back toward editorial.
const serifAccent = Lora({
 subsets: ['latin'],
 weight: ['400', '500', '600'],
 style: ['italic'],
 variable: '--font-serif',
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
 // `dark:` variant in. Light mode is deliberately off — the
 // brand voice committed on 2026-05-17 is warm-dark (bamboo-900
 // surface, bamboo-50 ink) inspired by Linear's dark-first app
 // pairing with Tailscale's cream-warm marketing palette.
 return (
 <html
 lang={locale}
 className={`dark ${wordmarkFont.variable} ${bodySans.variable} ${serifAccent.variable}`}
 >
 <body className="min-h-screen bg-ink-950 font-sans text-bamboo-50 antialiased">
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
