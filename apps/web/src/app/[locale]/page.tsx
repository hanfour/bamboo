// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { fetchActivity, fetchOverview, type ApiOverview } from '@/lib/api';
import type { ActivityEvent } from '@/lib/types';
import { ActivityFeed } from '@/components/ActivityFeed';
import { FetchErrorState } from '@/components/FetchErrorState';

export default async function DashboardPage() {
  const [overview, activity] = await Promise.all([fetchOverview(), fetchActivity(20)]);

  // The dashboard's primary signal is the overview card stack; if
  // that fetch fails we surface the auth/network state explicitly
  // rather than rendering zeroed-out cards that look like a healthy
  // empty tenant. Activity is a side-section — if only it failed we
  // could degrade by hiding it, but in practice both reads go through
  // the same controller so they tend to fail together.
  if (overview.kind !== 'ok') {
    return <FetchErrorState kind={overview.kind} />;
  }

  return (
    <Dashboard
      overview={overview.value}
      activity={activity.kind === 'ok' ? activity.value : []}
    />
  );
}

function Dashboard({
  overview,
  activity,
}: {
  overview: ApiOverview;
  activity: ActivityEvent[];
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
          label={t('openRecommendations')}
          value={overview.recommendationCount}
          tone={overview.recommendationCount > 0 ? 'amber' : undefined}
        />
      </div>

      <section className="space-y-2">
        <h2 className="text-sm font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
          {t('recentActivity')}
        </h2>
        <ActivityFeed events={activity} />
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
