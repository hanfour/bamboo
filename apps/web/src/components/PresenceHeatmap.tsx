// SPDX-License-Identifier: AGPL-3.0-or-later
'use client';

import { useMemo, useState } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import {
  buildGrid,
  computeStats,
  formatDuration,
  sumByDay,
  type PresenceEntry,
} from '@/lib/presence';

// Empty cells and the blue ramp follow the Cursor dashboard's activity
// chart. Index = level 0..4.
const LEVEL_CLASS = [
  'bg-[#777]/15',
  'bg-[var(--heat-1)]',
  'bg-[var(--heat-2)]',
  'bg-[var(--heat-3)]',
  'bg-[var(--heat-4)]',
];

type PeerRef = { id: string; hostname: string };

export function PresenceHeatmap({
  entries,
  peers,
  today,
}: {
  entries: PresenceEntry[];
  peers: PeerRef[];
  today: string;
}) {
  const t = useTranslations('dashboard.presence');
  const locale = useLocale();
  const [selected, setSelected] = useState<string>('all');

  // Busiest nodes first, so the tabs that matter stay on screen.
  const tabs = useMemo(() => {
    const totals = new Map<string, number>();
    for (const e of entries) totals.set(e.peerId, (totals.get(e.peerId) ?? 0) + e.seconds);
    return [...peers].sort((a, b) => (totals.get(b.id) ?? 0) - (totals.get(a.id) ?? 0));
  }, [entries, peers]);

  const byDay = useMemo(
    () => sumByDay(entries, selected === 'all' ? undefined : selected),
    [entries, selected],
  );
  const grid = useMemo(() => buildGrid(byDay, today), [byDay, today]);
  const stats = useMemo(() => computeStats(byDay, today), [byDay, today]);

  const monthFmt = useMemo(() => new Intl.DateTimeFormat(locale, { month: 'short', timeZone: 'UTC' }), [locale]);
  const longMonthFmt = useMemo(
    () => new Intl.DateTimeFormat(locale, { month: 'long', year: 'numeric', timeZone: 'UTC' }),
    [locale],
  );
  const dayFmt = useMemo(
    () => new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeZone: 'UTC' }),
    [locale],
  );
  const asDate = (day: string) => new Date(`${day}T00:00:00Z`);

  // A month label sits over the first column that starts that month. A
  // label whose neighbour also starts a month is dropped: the leading
  // partial week would otherwise overlap the next month's name.
  const starts = grid.map((col, i) => {
    const first = col.find((c) => c !== null);
    if (!first) return null;
    const prev = i > 0 ? grid[i - 1].find((c) => c !== null) : null;
    return !prev || prev.day.slice(0, 7) !== first.day.slice(0, 7) ? first.day : null;
  });
  const monthLabels = starts.map((day, i) =>
    day && !starts[i + 1] ? monthFmt.format(asDate(day)) : '',
  );

  const dash = '—';
  const summary: [string, string][] = [
    [t('mostActiveMonth'), stats.mostActiveMonth ? longMonthFmt.format(asDate(`${stats.mostActiveMonth}-01`)) : dash],
    [t('mostActiveDay'), stats.mostActiveDay ? dayFmt.format(asDate(stats.mostActiveDay)) : dash],
    [t('longestStreak'), stats.longestStreak > 0 ? t('days', { count: stats.longestStreak }) : dash],
    [t('currentStreak'), stats.currentStreak > 0 ? t('days', { count: stats.currentStreak }) : dash],
  ];

  return (
    <div>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-[13px] text-bamboo-200/60">{t('title')}</h2>
          <div className="mt-2 text-3xl font-medium tabular-nums tracking-tight text-bamboo-50">
            {formatDuration(stats.totalSeconds)}
          </div>
        </div>
        <div
          role="tablist"
          aria-label={t('title')}
          className="flex max-w-full items-center gap-1 overflow-x-auto rounded-full bg-[#777]/15 p-1"
        >
          {[{ id: 'all', hostname: t('all') }, ...tabs].map((tab) => (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={selected === tab.id}
              onClick={() => setSelected(tab.id)}
              className={[
                'h-8 shrink-0 rounded-full px-4 text-[13px] transition-colors',
                selected === tab.id
                  ? 'bg-[#777]/35 text-bamboo-50'
                  : 'text-bamboo-200/60 hover:text-bamboo-50',
              ].join(' ')}
            >
              {tab.hostname}
            </button>
          ))}
        </div>
      </div>

      <div className="mt-6 overflow-x-auto">
        <div className="min-w-[640px]">
          <div className="flex gap-[3px] pb-2 text-xs text-bamboo-200/50" aria-hidden="true">
            {monthLabels.map((label, i) => (
              <div key={i} className="relative h-4 flex-1">
                <span className="absolute left-0 whitespace-nowrap">{label}</span>
              </div>
            ))}
          </div>
          <div className="flex gap-[3px]" role="img" aria-label={t('title')}>
            {grid.map((col, ci) => (
              <div key={ci} className="flex flex-1 flex-col gap-[3px]">
                {col.map((cell, ri) =>
                  cell ? (
                    <div
                      key={ri}
                      title={`${dayFmt.format(asDate(cell.day))}: ${cell.seconds > 0 ? formatDuration(cell.seconds) : t('none')}`}
                      className={`aspect-square rounded-[3px] ${LEVEL_CLASS[cell.level]}`}
                    />
                  ) : (
                    <div key={ri} className="aspect-square" />
                  ),
                )}
              </div>
            ))}
          </div>
        </div>
      </div>

      <dl className="mt-6 grid grid-cols-2 gap-4 text-[13px] lg:grid-cols-4">
        {summary.map(([label, value]) => (
          <div key={label}>
            <dt className="text-bamboo-200/60">{label}</dt>
            <dd className="mt-1 text-bamboo-50">{value}</dd>
          </div>
        ))}
      </dl>

      <div className="mt-5 flex items-center gap-1.5 text-xs text-bamboo-200/50">
        <span>{t('less')}</span>
        {LEVEL_CLASS.map((c, i) => (
          <span key={i} className={`h-3 w-3 rounded-[3px] ${c}`} />
        ))}
        <span>{t('more')}</span>
      </div>
    </div>
  );
}
