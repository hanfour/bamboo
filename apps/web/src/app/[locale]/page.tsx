// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { fetchOverview } from '@/lib/api';

export default async function DashboardPage() {
  const overview = await fetchOverview();
  return <Dashboard overview={overview} />;
}

function Dashboard({
  overview,
}: {
  overview: Awaited<ReturnType<typeof fetchOverview>>;
}) {
  const t = useTranslations('dashboard');

  return (
    <div className="space-y-8">
      <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>

      <div className="grid gap-4 sm:grid-cols-3">
        <Stat label={t('totalPeers')} value={overview.totalPeers} />
        <Stat label={t('online')} value={overview.onlinePeers} tone="bamboo" />
        <Stat label={t('offline')} value={overview.offlinePeers} />
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Stat label={t('policyRevision')} value={overview.policyRevision} mono />
        <Stat
          label="Open recommendations"
          value={overview.recommendationCount}
          tone={overview.recommendationCount > 0 ? 'amber' : undefined}
        />
      </div>

      <section className="space-y-2">
        <h2 className="text-sm font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
          {t('recentActivity')}
        </h2>
        <p className="text-sm text-zinc-500 dark:text-zinc-400">
          {t('noActivity')}
        </p>
      </section>
    </div>
  );
}

function Stat({
  label,
  value,
  mono = true,
  tone,
}: {
  label: string;
  value: number;
  mono?: boolean;
  tone?: 'bamboo' | 'amber';
}) {
  const valueClass = [
    'mt-1 text-3xl font-semibold',
    mono ? 'font-mono' : '',
    tone === 'bamboo' ? 'text-bamboo-600 dark:text-bamboo-400' : '',
    tone === 'amber' ? 'text-amber-600 dark:text-amber-400' : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className="rounded-lg border border-zinc-200 p-4 dark:border-zinc-800">
      <div className="text-xs uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
        {label}
      </div>
      <div className={valueClass}>{value}</div>
    </div>
  );
}
