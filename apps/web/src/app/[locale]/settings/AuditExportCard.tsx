// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useTranslations } from 'next-intl';

// AuditExportCard renders the three preset download buttons for
// the admin audit-log CSV export (PR #206). It lives in its own
// 'use client' module so the `since` lower bound is computed at
// CLICK time, not render time — a server-rendered page can sit in
// a tab for hours, and "Last 30 days" must mean "30 days from
// when I click", not "30 days from when the page rendered".
//
// The buttons hand off via window.location.assign so the browser
// drives the download via the controller's Content-Disposition;
// no JS fetch + Blob plumbing required, and the page itself stays
// available (the navigation is treated as an attachment download
// by the browser, not a navigation).
export function AuditExportCard() {
  const t = useTranslations('settings.auditExportCard');
  const download = (daysBack: number | null) => {
    const url = new URL('/api/audit-export', window.location.origin);
    if (daysBack != null) {
      const since = new Date(Date.now() - daysBack * 86_400_000).toISOString();
      url.searchParams.set('since', since);
    }
    window.location.assign(url.toString());
  };
  return (
    <div className="space-y-3 rounded-lg border border-ink-800 bg-ink-950 p-4">
      <div className="space-y-1">
        <h3 className="text-sm font-medium text-bamboo-50">{t('title')}</h3>
        <p className="text-sm text-bamboo-200/70">{t('description')}</p>
      </div>
      <div className="flex flex-wrap gap-2">
        <DownloadButton onClick={() => download(null)} label={t('range.last7d')} />
        <DownloadButton onClick={() => download(30)} label={t('range.last30d')} />
        <DownloadButton onClick={() => download(90)} label={t('range.last90d')} />
      </div>
      <p className="text-xs text-bamboo-200/50">{t('hint')}</p>
    </div>
  );
}

function DownloadButton({ onClick, label }: { onClick: () => void; label: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex items-center rounded-md border border-bamboo-300/40 bg-bamboo-50 px-3 py-1.5 text-sm font-medium text-ink-950 transition-colors hover:bg-bamboo-100"
    >
      {label}
    </button>
  );
}
