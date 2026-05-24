// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { Link } from '@/i18n/routing';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchMe } from '@/lib/api';

// Settings is the home for tenant + account configuration. P0-3
// skeleton — shows tenant ID / slug, account email + role, and a
// link card to the Pre-auth keys management page (which now lives
// under Settings in the IA per the 2026-05 roadmap, matching how
// Tailscale nests its keys under Settings → Keys).
//
// Future P1 sections will land here too: DNS, webhooks, API tokens,
// trust credentials. The Section / Field components below are kept
// inline since this is the only consumer for now; promote to shared
// primitives if a third surface needs them.
export default async function SettingsPage() {
 const me = await fetchMe();
 if (!me.authenticated) {
 return <FetchErrorState kind="unauthorized" />;
 }
 return (
 <SettingsView
 email={me.email ?? ''}
 tenantId={me.tenantId}
 tenantSlug={me.tenantSlug}
 isAdmin={Boolean(me.isAdmin)}
 />
 );
}

function SettingsView({
 email,
 tenantId,
 tenantSlug,
 isAdmin,
}: {
 email: string;
 tenantId: string;
 tenantSlug: string;
 isAdmin: boolean;
}) {
 const t = useTranslations('settings');
 return (
 <div className="space-y-8">
 <header>
 <h1 className="text-4xl font-light tracking-tight text-bamboo-50 sm:text-5xl">{t("title")}<span className="ml-3 font-serif italic font-normal text-bamboo-300/80">{t("titleAccent")}</span></h1>
 <p className="mt-1 text-sm text-bamboo-200/70">{t('subtitle')}</p>
 </header>

 <Section title={t('tenant.title')}>
 <Field label={t('tenant.id')} value={tenantId} mono />
 <Field label={t('tenant.slug')} value={tenantSlug} mono />
 </Section>

 <Section title={t('account.title')}>
 <Field label={t('account.email')} value={email} />
 <Field label={t('account.role')} value={<RoleBadge admin={isAdmin} />} />
 </Section>

 <Section title={t('preAuthKeysCard.title')}>
 <PreAuthKeysCard />
 </Section>

 <Section title={t('webhooksCard.title')}>
 <WebhooksCard />
 </Section>

 <Section title={t('apiTokensCard.title')}>
 <APITokensCard />
 </Section>
 </div>
 );
}

function RoleBadge({ admin }: { admin: boolean }) {
 const t = useTranslations('settings.account');
 // Outlined badge in the same vocabulary as PeerTable / PreAuthKeyTable
 // status dots — bamboo-300 outline + bamboo-700 text for admin
 // (single brand accent), zinc outline + zinc-700 text for member.
 return admin ? (
 <span className="inline-flex items-center rounded-full border border-bamboo-300 px-2 py-0.5 text-xs font-medium text-bamboo-700 dark:border-bamboo-700 dark:text-bamboo-300">
 {t('admin')}
 </span>
 ) : (
 <span className="inline-flex items-center rounded-full border border-bamboo-200/30 px-2 py-0.5 text-xs font-medium text-bamboo-100 dark:text-bamboo-100">
 {t('member')}
 </span>
 );
}

function PreAuthKeysCard() {
 const t = useTranslations('settings.preAuthKeysCard');
 return (
 <Link
 href="/preauth-keys"
 className="group flex items-start justify-between gap-4 rounded-lg border border-ink-800 bg-ink-950 p-4 transition-colors hover:border-zinc-300 dark:bg-ink-950 dark:hover:border-ink-700"
 >
 <div className="min-w-0">
 <h3 className="text-sm font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1 text-sm text-bamboo-200/70">{t('description')}</p>
 <p className="mt-2 text-xs font-medium text-bamboo-700 group-hover:text-bamboo-800 dark:text-bamboo-300 dark:group-hover:text-bamboo-200">
 {t('link')} →
 </p>
 </div>
 </Link>
 );
}

function WebhooksCard() {
 const t = useTranslations('settings.webhooksCard');
 return (
 <Link
 href="/webhooks"
 className="group flex items-start justify-between gap-4 rounded-lg border border-ink-800 bg-ink-950 p-4 transition-colors hover:border-zinc-300 dark:bg-ink-950 dark:hover:border-ink-700"
 >
 <div className="min-w-0">
 <h3 className="text-sm font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1 text-sm text-bamboo-200/70">{t('description')}</p>
 <p className="mt-2 text-xs font-medium text-bamboo-700 group-hover:text-bamboo-800 dark:text-bamboo-300 dark:group-hover:text-bamboo-200">
 {t('link')} →
 </p>
 </div>
 </Link>
 );
}

function APITokensCard() {
 const t = useTranslations('settings.apiTokensCard');
 return (
 <Link
 href="/api-tokens"
 className="group flex items-start justify-between gap-4 rounded-lg border border-ink-800 bg-ink-950 p-4 transition-colors hover:border-zinc-300 dark:bg-ink-950 dark:hover:border-ink-700"
 >
 <div className="min-w-0">
 <h3 className="text-sm font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1 text-sm text-bamboo-200/70">{t('description')}</p>
 <p className="mt-2 text-xs font-medium text-bamboo-700 group-hover:text-bamboo-800 dark:text-bamboo-300 dark:group-hover:text-bamboo-200">
 {t('link')} →
 </p>
 </div>
 </Link>
 );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
 return (
 <section className="space-y-3">
 <h2 className="text-xs font-medium uppercase tracking-wide text-bamboo-200/60">
 {title}
 </h2>
 <div className="space-y-2">{children}</div>
 </section>
 );
}

function Field({
 label,
 value,
 mono = false,
}: {
 label: string;
 value: React.ReactNode;
 mono?: boolean;
}) {
 return (
 <div className="grid grid-cols-[8rem_1fr] gap-3 text-sm">
 <dt className="text-bamboo-200/60">{label}</dt>
 <dd
 className={`min-w-0 break-words text-bamboo-50 ${
 mono ? 'font-mono text-xs' : ''
 }`}
 >
 {value}
 </dd>
 </div>
 );
}
