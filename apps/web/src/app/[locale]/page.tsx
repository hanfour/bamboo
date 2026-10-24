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
 <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">
 {t('title')}
 </h1>
 <p className="text-sm text-bamboo-200/60">{t('subtitle')}</p>
 </header>

 <section className="grid gap-3 sm:grid-cols-3">
 <Stat label={t('totalPeers')} value={overview.totalPeers} />
 <Stat label={t('online')} value={overview.onlinePeers} tone="online" />
 <Stat label={t('offline')} value={overview.offlinePeers} />
 </section>

 <section className="grid gap-3 sm:grid-cols-2">
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
 // tone='online' tints the number green for the online stat;
 // 'accent' tints it amber when recommendations are waiting (a soft
 // warning, not red). Plain values stay the primary text color.
 tone?: 'online' | 'accent';
}) {
 const valueClass = [
 'block text-4xl font-medium tabular-nums tracking-tight',
 tone === 'online' ? 'text-emerald-400' : '',
 tone === 'accent' ? 'text-amber-300' : '',
 !tone ? 'text-bamboo-50' : '',
 ]
 .filter(Boolean)
 .join(' ');

 return (
 <div className="space-y-3 rounded-2xl bg-ink-900 p-5">
 <span className="block text-xs text-bamboo-200/60">
 {label}
 </span>
 <span className={valueClass}>{value}</span>
 </div>
 );
}
