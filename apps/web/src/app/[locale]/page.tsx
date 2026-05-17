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

 // The page leads with the airy"總覽 / Overview" hero — a Linear-
 // style large light-weight head sitting in the warm-dark surface
 // with a serif italic accent on the inline noun. Stat cells
 // beneath are deliberately frameless; the numerals carry the
 // visual weight and the labels sit underneath as a quiet caption.
 return (
 <div className="space-y-12 pt-2">
 <header className="space-y-2">
 <h1 className="text-4xl font-light tracking-tight text-bamboo-50 sm:text-5xl">
 {t('title')}
 <span className="ml-3 font-serif italic font-normal text-bamboo-300/80">
 mesh
 </span>
 </h1>
 <p className="text-sm text-bamboo-200/60">{t('subtitle')}</p>
 </header>

 <section className="grid gap-x-10 gap-y-8 sm:grid-cols-3">
 <Stat label={t('totalPeers')} value={overview.totalPeers} />
 <Stat label={t('online')} value={overview.onlinePeers} tone="bamboo" />
 <Stat label={t('offline')} value={overview.offlinePeers} />
 </section>

 <section className="grid gap-x-10 gap-y-8 sm:grid-cols-2">
 <Stat label={t('policyRevision')} value={overview.policyRevision} />
 <Stat
 label={t('openRecommendations')}
 value={overview.recommendationCount}
 tone={overview.recommendationCount > 0 ? 'accent' : undefined}
 />
 </section>

 <section className="space-y-4">
 <h2 className="text-sm font-medium text-bamboo-200/70">
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
 tone,
}: {
 label: string;
 value: number;
 // tone='bamboo' brightens the number to the bamboo-300 accent for
 // the"online" stat; 'accent' shifts to a warmer ochre when the
 // recommendation count is non-zero (a soft warning, not red).
 // Plain values stay in the default bamboo-50 cream.
 tone?: 'bamboo' | 'accent';
}) {
 const valueClass = [
 'block text-5xl font-light tabular-nums tracking-tight',
 tone === 'bamboo' ? 'text-bamboo-300' : '',
 tone === 'accent' ? 'text-bamboo-400' : '',
 !tone ? 'text-bamboo-50' : '',
 ]
 .filter(Boolean)
 .join(' ');

 return (
 <div className="space-y-2">
 <span className="block text-xs font-medium tracking-wide text-bamboo-200/60">
 {label}
 </span>
 <span className={valueClass}>{value}</span>
 </div>
 );
}
