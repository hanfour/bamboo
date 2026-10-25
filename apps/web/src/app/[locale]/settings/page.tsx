// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { Link } from '@/i18n/routing';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchMe } from '@/lib/api';
import { AuditExportCard } from './AuditExportCard';

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
 <header className="space-y-1.5">
 <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">{t('title')}</h1>
 <p className="text-[13px] text-bamboo-200/60">{t('subtitle')}</p>
 </header>

 <section className="grid gap-4 lg:grid-cols-2">
 <InfoCard title={t('tenant.title')}>
 <Field label={t('tenant.id')} value={tenantId} mono />
 <Field label={t('tenant.slug')} value={tenantSlug} mono />
 </InfoCard>
 <InfoCard title={t('account.title')}>
 <Field label={t('account.email')} value={email} />
 <Field label={t('account.role')} value={<RoleBadge admin={isAdmin} />} />
 </InfoCard>
 </section>

 <section className="space-y-3">
 <h2 className="text-base font-medium text-bamboo-50">{t('sessionCard.title')}</h2>
 <SessionCard issuedAt={issuedAt} expiresAt={expiresAt} oidcProvider={oidcProvider} />
 </section>

 <section className="space-y-3">
 <h2 className="text-base font-medium text-bamboo-50">{t('manageTitle')}</h2>
 <div className="grid gap-4 sm:grid-cols-2">
 <PreAuthKeysCard />
 <APITokensCard />
 <WebhooksCard />
 {isAdmin && <RelaysCard />}
 </div>
 </section>

 {isAdmin && (
 <section className="space-y-3">
 <h2 className="text-base font-medium text-bamboo-50">{t('auditExportCard.title')}</h2>
 <AuditExportCard />
 </section>
 )}
 </div>
 );
}

function RoleBadge({ admin }: { admin: boolean }) {
 const t = useTranslations('settings.account');
 return (
 <span
 className={[
 'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium',
 admin ? 'bg-bamboo-50 text-ink-950' : 'bg-[#777]/25 text-bamboo-100',
 ].join(' ')}
 >
 {admin ? t('admin') : t('member')}
 </span>
 );
}

function PreAuthKeysCard() {
 const t = useTranslations('settings.preAuthKeysCard');
 return (
 <Link
 href="/preauth-keys"
 className="group flex h-full items-start justify-between gap-4 rounded-2xl bg-ink-900 p-5 transition-colors hover:bg-ink-850"
 >
 <div className="min-w-0">
 <h3 className="text-[15px] font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1.5 text-[13px] leading-5 text-bamboo-200/60">{t('description')}</p>
 <p className="mt-3 text-xs font-medium text-bamboo-200/80 group-hover:text-bamboo-50">
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
 className="group flex h-full items-start justify-between gap-4 rounded-2xl bg-ink-900 p-5 transition-colors hover:bg-ink-850"
 >
 <div className="min-w-0">
 <h3 className="text-[15px] font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1.5 text-[13px] leading-5 text-bamboo-200/60">{t('description')}</p>
 <p className="mt-3 text-xs font-medium text-bamboo-200/80 group-hover:text-bamboo-50">
 {t('link')} →
 </p>
 </div>
 </Link>
 );
}

function RelaysCard() {
 const t = useTranslations('settings.relaysCard');
 return (
 <Link
 href="/relays"
 className="group flex h-full items-start justify-between gap-4 rounded-2xl bg-ink-900 p-5 transition-colors hover:bg-ink-850"
 >
 <div className="min-w-0">
 <h3 className="text-[15px] font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1.5 text-[13px] leading-5 text-bamboo-200/60">{t('description')}</p>
 <p className="mt-3 text-xs font-medium text-bamboo-200/80 group-hover:text-bamboo-50">
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
 className="group flex h-full items-start justify-between gap-4 rounded-2xl bg-ink-900 p-5 transition-colors hover:bg-ink-850"
 >
 <div className="min-w-0">
 <h3 className="text-[15px] font-medium text-bamboo-50">{t('title')}</h3>
 <p className="mt-1.5 text-[13px] leading-5 text-bamboo-200/60">{t('description')}</p>
 <p className="mt-3 text-xs font-medium text-bamboo-200/80 group-hover:text-bamboo-50">
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
 <div className="space-y-3 rounded-2xl bg-ink-900 p-5">
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
 className="inline-flex h-8 items-center rounded-full bg-[#777]/20 px-4 text-[13px] font-medium text-bamboo-50 transition-colors hover:bg-[#777]/30"
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

function InfoCard({ title, children }: { title: string; children: React.ReactNode }) {
 return (
 <div className="space-y-4 rounded-2xl bg-ink-900 p-5">
 <h2 className="text-[13px] text-bamboo-200/60">{title}</h2>
 <div className="space-y-3">{children}</div>
 </div>
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
 <div className="grid grid-cols-[7rem_1fr] gap-3 text-[13px]">
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
