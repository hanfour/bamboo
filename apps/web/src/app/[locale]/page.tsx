// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { fetchActivity, fetchOverview, type ApiOverview } from '@/lib/api';
import type { ActivityEvent } from '@/lib/types';
import { ActivityFeed } from '@/components/ActivityFeed';
import { FetchErrorState } from '@/components/FetchErrorState';
import { PageHeader } from '@/components/PageHeader';

export default async function DashboardPage() {
  const [overview, activity] = await Promise.all([fetchOverview(), fetchActivity(20)]);

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
    <div>
      <PageHeader kicker="overview · 總覽" title={t('title')} />

      {/* Stat grid: editorial-style metric blocks separated by hairlines.
          The mono digits + serif-feel labels keep the eye on the number. */}
      <section className="grid grid-cols-2 gap-x-0 gap-y-8 border-y border-white/[0.06] py-8 sm:grid-cols-5">
        <Stat label={t('totalPeers')} value={overview.totalPeers} />
        <Stat
          label={t('online')}
          value={overview.onlinePeers}
          accent
        />
        <Stat label={t('offline')} value={overview.offlinePeers} />
        <Stat label={t('policyRevision')} value={overview.policyRevision} />
        <Stat
          label="recommendations"
          value={overview.recommendationCount}
          tone={overview.recommendationCount > 0 ? 'amber' : undefined}
        />
      </section>

      <section className="mt-14">
        <SectionHeading>{t('recentActivity')}</SectionHeading>
        <ActivityFeed events={activity} />
      </section>
    </div>
  );
}

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-5 flex items-baseline gap-3">
      <span aria-hidden className="h-px flex-1 bg-white/[0.06]" />
      <h2 className="font-mono text-[11px] uppercase tracking-[0.24em] text-zinc-500">
        {children}
      </h2>
      <span aria-hidden className="h-px w-12 bg-white/[0.06]" />
    </div>
  );
}

function Stat({
  label,
  value,
  accent = false,
  tone,
}: {
  label: string;
  value: number;
  accent?: boolean;
  tone?: 'amber';
}) {
  const valueClass = [
    'font-mono font-light leading-none',
    'text-4xl sm:text-5xl',
    accent ? 'text-bamboo-300' : '',
    tone === 'amber' ? 'text-amber-300' : '',
    !accent && !tone ? 'text-zinc-100' : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className="relative px-1 sm:px-6 sm:[&:not(:first-child)]:border-l sm:[&:not(:first-child)]:border-white/[0.06]">
      <span className={valueClass}>{String(value).padStart(2, '0')}</span>
      <p className="mt-3 font-mono text-[10px] uppercase tracking-[0.22em] text-zinc-500">
        {label}
      </p>
    </div>
  );
}
