// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from 'react';
import { useTranslations } from 'next-intl';
import { Link } from '@/i18n/routing';
import { fetchActivity, fetchOverview, fetchPeers, fetchPresence, type ApiOverview } from '@/lib/api';
import type { PresenceEntry } from '@/lib/presence';
import type { ActivityEvent } from '@/lib/types';
import { ActivityFeed } from '@/components/ActivityFeed';
import { FetchErrorState } from '@/components/FetchErrorState';
import { PresenceHeatmap } from '@/components/PresenceHeatmap';

export default async function DashboardPage() {
 const [overview, activity, presence, peers] = await Promise.all([
 fetchOverview(),
 fetchActivity(20),
 fetchPresence(),
 fetchPeers(),
 ]);

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
 presence={presence}
 peers={peers.kind === 'ok' ? peers.value.peers.map((p) => ({ id: p.id, hostname: p.hostname })) : []}
 today={new Date().toISOString().slice(0, 10)}
 />
 );
}

function Dashboard({
 overview,
 activity,
 presence,
 peers,
 today,
}: {
 overview: ApiOverview;
 activity: ActivityEvent[];
 presence: PresenceEntry[];
 peers: { id: string; hostname: string }[];
 today: string;
}) {
 const t = useTranslations('dashboard');
 const total = overview.totalPeers;
 const pct = total > 0 ? Math.round((overview.onlinePeers / total) * 100) : 0;

 // Layout follows the Cursor dashboard overview: a title, two large
 // cards, a row of link tiles, then a wide card. Surfaces are #111
 // rounded cards on the black page, with no borders.
 return (
 <div className="space-y-6">
 <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">{t('title')}</h1>

 <section className="grid gap-4 lg:grid-cols-2">
 <Card>
 <CardHead label={t('networkCard')}>
 <PillLink href="/peers" primary>{t('managePeers')}</PillLink>
 </CardHead>
 <div className="mt-5 flex items-baseline gap-2">
 <span className="text-3xl font-medium tabular-nums tracking-tight text-bamboo-50">
 {overview.onlinePeers}
 <span className="text-bamboo-200/50"> / {total}</span>
 </span>
 <span className="text-[13px] text-bamboo-200/60">{t('online')}</span>
 </div>
 <div
 className="mt-4 h-1 overflow-hidden rounded-full bg-[#777]/25"
 role="progressbar"
 aria-valuenow={pct}
 aria-valuemin={0}
 aria-valuemax={100}
 >
 <div className="h-full rounded-full bg-bamboo-50" style={{ width: `${pct}%` }} />
 </div>
 <p className="mt-4 text-[13px] text-bamboo-200/60">
 {t('offlineCount', { count: overview.offlinePeers })}
 </p>
 </Card>

 <Card>
 <CardHead label={t('policyCard')}>
 <PillLink href="/acl" primary>{t('editPolicy')}</PillLink>
 </CardHead>
 <div className="mt-5 text-3xl font-medium tabular-nums tracking-tight text-bamboo-50">
 {t('policyRevisionValue', { revision: overview.policyRevision })}
 </div>
 <dl className="mt-5 space-y-3 text-[13px]">
 <div className="flex items-center justify-between">
 <dt className="text-bamboo-200/60">{t('openRecommendations')}</dt>
 <dd
 className={
 overview.recommendationCount > 0
 ? 'font-medium text-amber-300'
 : 'text-bamboo-50'
 }
 >
 {overview.recommendationCount}
 </dd>
 </div>
 </dl>
 </Card>
 </section>

 <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
 <Tile href="/peers" title={t('tiles.peers.title')} desc={t('tiles.peers.desc')} icon={<TileServer />} />
 <Tile href="/users" title={t('tiles.users.title')} desc={t('tiles.users.desc')} icon={<TileUsers />} />
 <Tile href="/acl" title={t('tiles.acl.title')} desc={t('tiles.acl.desc')} icon={<TileShield />} />
 <Tile href="/dns" title={t('tiles.dns.title')} desc={t('tiles.dns.desc')} icon={<TileGlobe />} />
 </section>

 <Card>
 <PresenceHeatmap entries={presence} peers={peers} today={today} />
 </Card>

 <Card>
 <h2 className="mb-4 text-[13px] text-bamboo-200/60">{t('recentActivity')}</h2>
 <ActivityFeed events={activity} />
 </Card>
 </div>
 );
}

function Card({ children }: { children: ReactNode }) {
 return <div className="rounded-2xl bg-ink-900 p-6">{children}</div>;
}

function CardHead({ label, children }: { label: string; children?: ReactNode }) {
 return (
 <div className="flex min-h-8 items-center justify-between gap-3">
 <h2 className="text-[13px] text-bamboo-200/60">{label}</h2>
 {children}
 </div>
 );
}

function PillLink({ href, primary, children }: { href: '/peers' | '/acl'; primary?: boolean; children: ReactNode }) {
 return (
 <Link
 href={href}
 className={[
 'inline-flex h-8 items-center rounded-full px-4 text-[13px] font-medium transition-colors',
 primary
 ? 'bg-bamboo-50 text-ink-950 hover:bg-bamboo-100'
 : 'bg-[#777]/20 text-bamboo-50 hover:bg-[#777]/30',
 ].join(' ')}
 >
 {children}
 </Link>
 );
}

function Tile({
 href,
 title,
 desc,
 icon,
}: {
 href: '/peers' | '/users' | '/acl' | '/dns';
 title: string;
 desc: string;
 icon: ReactNode;
}) {
 return (
 <Link
 href={href}
 className="group flex flex-col gap-6 rounded-2xl bg-ink-900 p-5 transition-colors hover:bg-[#181818]"
 >
 <div className="flex items-start justify-between text-bamboo-50">
 {icon}
 <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4 text-bamboo-200/40 transition-colors group-hover:text-bamboo-50" aria-hidden>
 <path d="M7 17L17 7" />
 <path d="M8 7h9v9" />
 </svg>
 </div>
 <div className="space-y-1">
 <div className="text-[15px] font-medium text-bamboo-50">{title}</div>
 <div className="text-[13px] leading-5 text-bamboo-200/60">{desc}</div>
 </div>
 </Link>
 );
}

const ICON = { viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.5, strokeLinecap: 'round', strokeLinejoin: 'round', className: 'h-6 w-6', 'aria-hidden': true } as const;
function TileServer() {
 return (<svg {...ICON}><rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" /></svg>);
}
function TileUsers() {
 return (<svg {...ICON}><circle cx="9" cy="8" r="3.5" /><path d="M2.5 20c.6-3.4 3.2-5.5 6.5-5.5s5.9 2.1 6.5 5.5" /><path d="M16 4.7a3.5 3.5 0 0 1 0 6.6M18 14.8c1.9.7 3.2 2.4 3.6 5.2" /></svg>);
}
function TileShield() {
 return (<svg {...ICON}><path d="M12 3l7.5 3v5.5c0 4.5-3.1 8.2-7.5 9.5-4.4-1.3-7.5-5-7.5-9.5V6z" /></svg>);
}
function TileGlobe() {
 return (<svg {...ICON}><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3c2.5 2.7 3.8 5.7 3.8 9S14.5 18.3 12 21c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z" /></svg>);
}
