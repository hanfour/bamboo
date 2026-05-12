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
// "updatedAt = zero" indicates no row has been written yet — we
// display "Default (never customized)" rather than the ISO timestamp.
export default async function DNSPage() {
  const dns = await fetchDNS();
  if (dns.kind !== 'ok') {
    return <FetchErrorState kind={dns.kind} />;
  }
  return <DNSView dns={dns.value} />;
}

function DNSView({ dns }: { dns: DNSConfig }) {
  const t = useTranslations('dns');
  const everUpdated = dns.updatedAt && !dns.updatedAt.startsWith('0001-01-01');
  return (
    <div className="space-y-8">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
        <p className="mt-1 max-w-2xl text-sm text-zinc-600 dark:text-zinc-400">
          {t('subtitle')}
        </p>
      </header>

      <Section title={t('tenantSection')}>
        <Field label={t('tenantName')} value={dns.tailnetName} mono />
      </Section>

      <Section title={t('magicDnsSection')}>
        <Field
          label={t('magicDnsEnabled')}
          value={<StatusBadge enabled={dns.magicDnsEnabled} />}
        />
        <p className="text-xs text-zinc-500 dark:text-zinc-400">{t('magicDnsHint')}</p>
      </Section>

      <Section title={t('nameserversSection')}>
        {dns.globalNameservers.length === 0 ? (
          <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('nameserversEmpty')}</p>
        ) : (
          <ul className="space-y-1">
            {dns.globalNameservers.map((ns) => (
              <li
                key={ns}
                className="rounded border border-zinc-200 px-3 py-2 font-mono text-xs text-zinc-700 dark:border-zinc-800 dark:text-zinc-300"
              >
                {ns}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t('searchDomainsSection')}>
        {dns.searchDomains.length === 0 ? (
          <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('searchDomainsEmpty')}</p>
        ) : (
          <ul className="flex flex-wrap gap-1">
            {dns.searchDomains.map((d) => (
              <li
                key={d}
                className="rounded border border-zinc-200 px-2 py-0.5 text-xs text-zinc-600 dark:border-zinc-700 dark:text-zinc-400"
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
        <p className="text-xs text-zinc-500 dark:text-zinc-400">{t('overrideHint')}</p>
      </Section>

      <footer className="border-t border-zinc-200 pt-4 text-xs text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
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
    ? 'bg-bamboo-500'
    : 'border border-zinc-400 bg-transparent dark:border-zinc-500';
  return (
    <span className="inline-flex items-center gap-2 text-xs text-zinc-700 dark:text-zinc-300">
      <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dot}`} />
      {t(enabled ? 'enabled' : 'disabled')}
    </span>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-3">
      <h2 className="text-xs font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
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
      <dt className="text-zinc-500 dark:text-zinc-400">{label}</dt>
      <dd
        className={`min-w-0 break-words text-zinc-900 dark:text-zinc-100 ${
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
