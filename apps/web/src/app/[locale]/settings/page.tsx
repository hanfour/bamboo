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
 issuedAt={me.issuedAt}
 expiresAt={me.expiresAt}
 oidcProvider={me.oidcProvider}
 />
 );
}

function SettingsView({
 email,
 tenantId,
 tenantSlug,
 isAdmin,
 issuedAt,
 expiresAt,
 oidcProvider,
}: {
 email: string;
 tenantId: string;
 tenantSlug: string;
 isAdmin: boolean;
 issuedAt?: string;
 expiresAt?: string;
 oidcProvider?: string;
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

 <Section title={t('sessionCard.title')}>
 <SessionCard
 issuedAt={issuedAt}
 expiresAt={expiresAt}
 oidcProvider={oidcProvider}
 />
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

 {isAdmin && (
 <Section title={t('relaysCard.title')}>
 <RelaysCard />
 </Section>
 )}

 {isAdmin && (
 <Section title={t('auditExportCard.title')}>
 <AuditExportCard />
 </Section>
 )}
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

function AuditExportCard() {
 const t = useTranslations('settings.auditExportCard');
 // Three preset windows expressed as RFC3339 lower bounds, computed
 // at SSR time. The browser downloads on click; an empty href would
 // hit the controller's default (now-7d) so the "Last 7 days" link
 // can just point at the bare endpoint without a since param.
 const now = new Date();
 const ago = (days: number) =>
 new Date(now.getTime() - days * 24 * 60 * 60 * 1000).toISOString();
 return (
 <div className="space-y-3 rounded-lg border border-ink-800 bg-ink-950 p-4">
 <div className="space-y-1">
 <h3 className="text-sm font-medium text-bamboo-50">{t('title')}</h3>
 <p className="text-sm text-bamboo-200/70">{t('description')}</p>
 </div>
 <div className="flex flex-wrap gap-2">
 {/* Plain anchors — admin clicks, browser handles download via
   * the controller's Content-Disposition. No JS needed; works
   * even when the Settings page is dehydrated. */}
 <DownloadLink href="/api/audit-export" label={t('range.last7d')} />
 <DownloadLink href={`/api/audit-export?since=${encodeURIComponent(ago(30))}`} label={t('range.last30d')} />
 <DownloadLink href={`/api/audit-export?since=${encodeURIComponent(ago(90))}`} label={t('range.last90d')} />
 </div>
 <p className="text-xs text-bamboo-200/50">{t('hint')}</p>
 </div>
 );
}

function DownloadLink({ href, label }: { href: string; label: string }) {
 // download attribute lets the browser respect the Content-
 // Disposition filename from the controller without prompting
 // the user with a "Save link as" dialog.
 return (
 <a
 href={href}
 download
 className="inline-flex items-center rounded-md border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-ink-950 transition-colors hover:bg-bamboo-100"
 >
 {label}
 </a>
 );
}

function RelaysCard() {
 const t = useTranslations('settings.relaysCard');
 return (
 <Link
 href="/relays"
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

// CONTROLLER_BASE mirrors the TopBar value — sign-out is an HTTP
// (not Next) endpoint so we hit the controller directly. Server-side
// env so it's evaluated at request time, not bundled into the client.
const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';

// SessionCard renders three lines: who/when of the current session,
// time-to-expiry, and a sign-out link. Durations are rendered at
// server-render time and do not tick — acceptable for v1 (refresh
// works), simpler than introducing a client component.
function SessionCard({
 issuedAt,
 expiresAt,
 oidcProvider,
}: {
 issuedAt?: string;
 expiresAt?: string;
 oidcProvider?: string;
}) {
 const t = useTranslations('settings.sessionCard');
 const now = Date.now();
 const issuedMs = issuedAt ? new Date(issuedAt).getTime() : NaN;
 const expiresMs = expiresAt ? new Date(expiresAt).getTime() : NaN;
 const provider = formatProvider(oidcProvider, t);
 return (
 <div className="space-y-3 rounded-lg border border-ink-800 bg-ink-950 p-4 dark:bg-ink-950">
 <div className="space-y-1 text-sm text-bamboo-100">
 {Number.isFinite(issuedMs) ? (
 <p>{t('agePrefix', { value: formatDuration(now - issuedMs), provider })}</p>
 ) : null}
 {Number.isFinite(expiresMs) ? (
 expiresMs > now ? (
 <p className="text-bamboo-200/70">
 {t('expiresIn', { value: formatDuration(expiresMs - now) })}
 </p>
 ) : (
 <p className="text-amber-300">{t('expiresPast')}</p>
 )
 ) : null}
 </div>
 <a
 href={`${CONTROLLER_BASE}/auth/sign-out`}
 className="inline-flex items-center text-xs font-medium text-bamboo-700 transition-colors hover:text-bamboo-800 dark:text-bamboo-300 dark:hover:text-bamboo-200"
 >
 {t('signOut')} →
 </a>
 </div>
 );
}

function formatProvider(
 provider: string | undefined,
 t: ReturnType<typeof useTranslations>,
): string {
 if (!provider) return t('providerUnknown');
 // Capitalize known providers ("google" → "Google") for the prose
 // slot; unknown providers (dev-fallback, future SAML) pass through
 // unchanged so the operator can still identify the source.
 const known: Record<string, string> = { google: 'Google', github: 'GitHub' };
 const label = known[provider] ?? provider;
 return t('provider', { provider: label });
}

// formatDuration collapses a positive millisecond delta into the
// largest non-zero unit ("3h", "21m", "12s"). Matches the s/m/h/d
// vocabulary used by formatRelative in UsersTable so the two
// surfaces feel coherent.
function formatDuration(ms: number): string {
 if (!Number.isFinite(ms) || ms < 0) return '—';
 const s = Math.round(ms / 1000);
 if (s < 60) return `${s}s`;
 if (s < 3600) return `${Math.round(s / 60)}m`;
 if (s < 86_400) return `${Math.round(s / 3600)}h`;
 return `${Math.round(s / 86_400)}d`;
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
