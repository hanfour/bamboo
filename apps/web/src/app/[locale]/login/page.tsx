// SPDX-License-Identifier: AGPL-3.0-or-later

import { getTranslations, setRequestLocale } from 'next-intl/server';

import { redirect } from '@/i18n/routing';
import { fetchMe } from '@/lib/api';
import { controllerPublicUrl } from '@/lib/publicUrl';


// The sign-in page. It stands alone: ChromeShell drops the sidebar on
// this route. Someone who is already signed in goes to the overview.
export default async function LoginPage({ params }: { params: Promise<{ locale: string }> }) {
 const { locale } = await params;
 setRequestLocale(locale);
 const me = await fetchMe();
 if (me.authenticated) redirect({ href: '/', locale });

 const t = await getTranslations('landing');
 const tl = await getTranslations('login');

 return (
 <div className="flex min-h-[calc(100vh-4rem)] items-center justify-center">
 <div className="w-full max-w-sm rounded-2xl bg-ink-900 p-8">
 <div className="text-[17px] font-semibold leading-none tracking-tight text-bamboo-50">
 bamboo
 </div>
 <h1 className="mt-8 text-2xl font-medium tracking-tight text-bamboo-50">{tl('title')}</h1>
 <p className="mt-2 text-[13px] text-bamboo-200/60">{t('subtitle')}</p>
 <a
 href={controllerPublicUrl('/auth/google/login')}
 className="mt-8 flex h-10 w-full items-center justify-center gap-2 rounded-full bg-bamboo-50 text-[13px] font-medium text-ink-950 transition-colors hover:bg-bamboo-100"
 >
 <GoogleMark />
 {t('signInWithGoogle')}
 </a>
 <p className="mt-6 text-xs leading-5 text-bamboo-200/50">{t('selfHosted')}</p>
 </div>
 </div>
 );
}

function GoogleMark() {
 return (
 <svg viewBox="0 0 24 24" className="h-4 w-4" aria-hidden>
 <path fill="#4285F4" d="M22.5 12.2c0-.8-.1-1.5-.2-2.2H12v4.2h5.9a5 5 0 0 1-2.2 3.3v2.7h3.5c2.1-1.9 3.3-4.7 3.3-8z" />
 <path fill="#34A853" d="M12 23c3 0 5.5-1 7.3-2.7l-3.5-2.7c-1 .7-2.2 1.1-3.8 1.1-2.9 0-5.4-2-6.3-4.6H2.1v2.8A11 11 0 0 0 12 23z" />
 <path fill="#FBBC05" d="M5.7 14.1a6.6 6.6 0 0 1 0-4.2V7.1H2.1a11 11 0 0 0 0 9.8l3.6-2.8z" />
 <path fill="#EA4335" d="M12 5.4c1.6 0 3.1.6 4.3 1.7l3.2-3.2A11 11 0 0 0 2.1 7.1l3.6 2.8C6.6 7.4 9.100 5.400 12 5.400z" />
 </svg>
 );
}
