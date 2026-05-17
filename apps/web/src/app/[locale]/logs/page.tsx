// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { LogsTable } from '@/components/LogsTable';
import { FetchErrorState } from '@/components/FetchErrorState';
import { fetchLogs } from '@/lib/api';
import type { LogEvent } from '@/lib/types';

// LogsPage renders the recent /api/v1/logs window — newest-first
// policy-evaluation traces (allow / deny decisions). Member-readable
// on the wire, so the only failure modes the FetchErrorState card
// will surface are unauthorized (no session) and unreachable.
// Dev environments without ClickHouse return an empty list — the
// LogsTable empty-state handles that gracefully.
export default async function LogsPage() {
 const events = await fetchLogs({ sinceHours: 24, limit: 100 });
 if (events.kind !== 'ok') {
 return <FetchErrorState kind={events.kind} />;
 }
 return <LogsView events={events.value} />;
}

function LogsView({ events }: { events: LogEvent[] }) {
 const t = useTranslations('logs');
 return (
 <div className="space-y-6">
 <header>
 <h1 className="text-4xl font-light tracking-tight text-bamboo-50 sm:text-5xl">{t("title")}<span className="ml-3 font-serif italic font-normal text-bamboo-300/80">{t("titleAccent")}</span></h1>
 <p className="mt-1 text-sm text-bamboo-200/70">
 {t('subtitle')}
 </p>
 </header>
 <LogsTable events={events} />
 </div>
 );
}
