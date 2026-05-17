// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchDNS } from '@/lib/api';
import type { DNSConfig } from '@/lib/types';

// DNS page surfaces the tenant's resolver config from /api/v1/dns.
// Read-only in v1 — the controller's PUT handler isn't shipped yet,
// and the data-plane MagicDNS implementation is the larger blocker.
//
// Layout mirrors Tailscale's /admin/dns: tailnet identifier on top,
// then MagicDNS toggle (rendered as a status badge here, not an
// interactive switch), then global nameservers + search domains.
//
//"updatedAt = zero" indicates no row has been written yet — we
// display"Default (never customized)" rather than the ISO timestamp.
export default async function DNSPage() {
 const dns = await fetchDNS();
 if (dns.kind === 'notFound') {
 // The controller returns 404 when no row exists in
 // `tenant_dns_config` for this tenant yet — i.e. MagicDNS has
 // never been touched. That's the dominant first-run state for
 // a fresh tenant, so the generic"resource not found" error
 // (rendered by FetchErrorState for `notFound`) actively
 // misleads here: nothing's broken, just unconfigured.
 //
 // Surface it as a hopeful empty state instead, with the same
 // page chrome (hero + serif italic accent) so the user
 // doesn't bounce between two visual styles.
 return <DNSEmptyState />;
 }
 if (dns.kind !== 'ok') {
 return <FetchErrorState kind={dns.kind} />;
 }
 return <DNSView dns={dns.value} />;
}

function DNSEmptyState() {
 const t = useTranslations('dns');
 return (
 <div className="space-y-10 pt-2">
 <header className="space-y-2">
 <h1 className="text-4xl font-light tracking-tight text-bamboo-50 sm:text-5xl">
 {t('title')}
 <span className="ml-3 font-serif italic font-normal text-bamboo-300/80">
 {t('titleAccent')}
 </span>
 </h1>
 <p className="max-w-2xl text-sm text-bamboo-200/60">{t('subtitle')}</p>
 </header>
 <section className="max-w-2xl space-y-4 rounded-md border border-ink-800 bg-ink-900/40 p-6">
 <h2 className="text-base font-medium tracking-tight text-bamboo-50">
 {t('empty.title')}
 </h2>
 <p className="text-sm leading-relaxed text-bamboo-200/70">
 {t('empty.body')}
 </p>
 <code className="block rounded border border-ink-800 bg-ink-950 px-3 py-2 font-mono text-xs text-bamboo-100">
 {t('empty.example')}
 </code>
 <p className="text-xs text-bamboo-200/60">{t('empty.note')}</p>
 </section>
 </div>
 );
}

function DNSView({ dns }: { dns: DNSConfig }) {
 const t = useTranslations('dns');
 const everUpdated = dns.updatedAt && !dns.updatedAt.startsWith('0001-01-01');
 return (
 <div className="space-y-10 pt-2">
 <header className="space-y-2">
 <h1 className="text-4xl font-light tracking-tight text-bamboo-50 sm:text-5xl">
 {t('title')}
 <span className="ml-3 font-serif italic font-normal text-bamboo-300/80">
 {t('titleAccent')}
 </span>
 </h1>
 <p className="max-w-2xl text-sm text-bamboo-200/60">{t('subtitle')}</p>
 </header>

 <Section title={t('tenantSection')}>
 <Field label={t('tenantName')} value={dns.tailnetName} mono />
 </Section>

 <Section title={t('magicDnsSection')}>
 <Field
 label={t('magicDnsEnabled')}
 value={<StatusBadge enabled={dns.magicDnsEnabled} />}
 />
 <p className="text-xs text-bamboo-200/60">{t('magicDnsHint')}</p>
 </Section>

 <Section title={t('nameserversSection')}>
 {dns.globalNameservers.length === 0 ? (
 <p className="text-sm text-bamboo-200/60">{t('nameserversEmpty')}</p>
 ) : (
 <ul className="space-y-1">
 {dns.globalNameservers.map((ns) => (
 <li
 key={ns}
 className="rounded border border-ink-800 bg-ink-900/40 px-3 py-2 font-mono text-xs text-bamboo-100"
 >
 {ns}
 </li>
 ))}
 </ul>
 )}
 </Section>

 <Section title={t('searchDomainsSection')}>
 {dns.searchDomains.length === 0 ? (
 <p className="text-sm text-bamboo-200/60">{t('searchDomainsEmpty')}</p>
 ) : (
 <ul className="flex flex-wrap gap-1">
 {dns.searchDomains.map((d) => (
 <li
 key={d}
 className="rounded border border-ink-800 bg-ink-900/40 px-2 py-0.5 text-xs text-bamboo-200/70"
 >
 {d}
 </li>
 ))}
 </ul>
 )}
 </Section>

 <Section title={t('overrideSection')}>
 <Field
 label={t('overrideEnabled')}
 value={<StatusBadge enabled={dns.overrideDnsServers} />}
 />
 <p className="text-xs text-bamboo-200/60">{t('overrideHint')}</p>
 </Section>

 <footer className="border-t border-ink-800 pt-4 text-xs text-bamboo-200/60">
 {everUpdated ? (
 <>
 {t('lastUpdated')}: <span className="font-mono">{formatTimestamp(dns.updatedAt)}</span>
 </>
 ) : (
 t('neverUpdated')
 )}
 </footer>
 </div>
 );
}

function StatusBadge({ enabled }: { enabled: boolean }) {
 const t = useTranslations('dns.status');
 const dot = enabled
 ? 'bg-bamboo-400'
 : 'border border-bamboo-200/40 bg-transparent';
 return (
 <span className="inline-flex items-center gap-2 text-xs text-bamboo-100">
 <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot}`} />
 {t(enabled ? 'enabled' : 'disabled')}
 </span>
 );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
 return (
 <section className="space-y-3">
 <h2 className="text-xs font-medium tracking-wide text-bamboo-200/60">
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
 <div className="grid grid-cols-[10rem_1fr] gap-3 text-sm">
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

function formatTimestamp(iso: string): string {
 const d = new Date(iso);
 if (Number.isNaN(d.getTime())) return '—';
 return d.toISOString().replace('T', ' ').replace(/\..+$/, ' UTC');
}
