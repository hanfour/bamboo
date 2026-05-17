// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useTranslations } from 'next-intl';
import type { LogEvent } from '@/lib/types';

// LogsTable renders recent policy-evaluation traces. The wire shape
// comes from /api/v1/logs (ClickHouse evaluation_traces, newest-
// first). Status follows the same dot+label vocabulary as
// PreAuthKeyTable / PeerTable for visual consistency: allow is a
// solid bamboo-500 dot (the"alive / passed" tone), deny is a hollow
// red ring (the"struck off" tone — same idiom as revoked keys, but
// red-toned because deny is a security-relevant decision).
export function LogsTable({ events }: { events: LogEvent[] }) {
 const t = useTranslations('logs');
 if (events.length === 0) {
 return (
 <p className="rounded-lg border border-dashed border-bamboo-200/30 p-8 text-center text-sm text-bamboo-200/60 dark:text-bamboo-200/40">
 {t('empty')}
 </p>
 );
 }
 return (
 <div className="overflow-x-auto rounded-lg border border-ink-800">
 <table className="w-full text-sm">
 <thead className="border-b border-ink-800 text-left text-xs font-medium uppercase tracking-wide text-bamboo-200/60 dark:text-bamboo-200/40">
 <tr>
 <th className="px-4 py-3 font-medium">{t('columns.occurredAt')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.source')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.destination')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.port')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.action')}</th>
 <th className="px-4 py-3 font-medium">{t('columns.rule')}</th>
 </tr>
 </thead>
 <tbody className="divide-y divide-ink-800/70">
 {events.map((e) => (
 <LogRow key={e.id} e={e} />
 ))}
 </tbody>
 </table>
 </div>
 );
}

function LogRow({ e }: { e: LogEvent }) {
 const t = useTranslations('logs');
 return (
 <tr className="text-bamboo-100">
 <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-bamboo-200/60">
 {formatTimestamp(e.occurredAt)}
 </td>
 <td className="px-4 py-3 font-mono text-xs text-bamboo-100">
 {e.source}
 </td>
 <td className="px-4 py-3 font-mono text-xs text-bamboo-100">
 {e.destination}
 </td>
 <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-bamboo-200/60">
 {e.port}
 {e.protocol ? `/${e.protocol.toLowerCase()}` : ''}
 </td>
 <td className="px-4 py-3">
 <ActionBadge action={e.action} t={t} />
 </td>
 <td className="px-4 py-3 font-mono text-xs text-bamboo-200/60">
 {e.matchedRuleId || <span className="text-bamboo-200/40">{t('noRule')}</span>}
 </td>
 </tr>
 );
}

function ActionBadge({
 action,
 t,
}: {
 action: LogEvent['action'];
 t: ReturnType<typeof useTranslations>;
}) {
 // allow → bamboo-500 solid dot ("alive / passed")
 // deny → hollow red ring (struck-off idiom, red-toned because the
 // decision is security-relevant). Avoids solid red, which
 // would break the Muji flat palette.
 const dotClass =
 action === 'allow'
 ? 'bg-bamboo-500'
 : 'border border-red-400 bg-transparent dark:border-red-500';
 const labelClass =
 action === 'allow'
 ? 'text-bamboo-100'
 : 'text-red-700 dark:text-red-400';
 return (
 <span className={`inline-flex items-center gap-2 text-xs ${labelClass}`}>
 <span aria-hidden className={`h-1.5 w-1.5 rounded-full ${dotClass}`} />
 {t(`action.${action}`)}
 </span>
 );
}

function formatTimestamp(iso: string): string {
 const d = new Date(iso);
 if (Number.isNaN(d.getTime())) return '—';
 return d.toISOString().replace('T', ' ').replace(/\..+$/, ' UTC');
}
