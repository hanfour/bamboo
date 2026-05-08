// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { mockPeers } from '@/lib/mockData';

export default function DashboardPage() {
  const t = useTranslations('dashboard');
  const total = mockPeers.length;
  const online = mockPeers.filter((p) => p.status === 'online').length;
  const offline = total - online;

  return (
    <div className="space-y-8">
      <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>

      <div className="grid gap-4 sm:grid-cols-3">
        <Stat label={t('totalPeers')} value={total} />
        <Stat label={t('online')} value={online} />
        <Stat label={t('offline')} value={offline} />
      </div>

      <section className="space-y-2">
        <h2 className="text-sm font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
          {t('recentActivity')}
        </h2>
        <p className="text-sm text-zinc-500 dark:text-zinc-400">{t('noActivity')}</p>
      </section>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border border-zinc-200 p-4 dark:border-zinc-800">
      <div className="text-xs uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
        {label}
      </div>
      <div className="mt-1 font-mono text-3xl font-semibold">{value}</div>
    </div>
  );
}
