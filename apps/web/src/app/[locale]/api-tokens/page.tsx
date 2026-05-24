// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { FetchErrorState } from '@/components/FetchErrorState';
import { APITokensView } from '@/components/APITokensView';
import { fetchAPITokens } from '@/lib/api';
import type { APIToken } from '@/lib/types';

// /api-tokens manages tenant-scoped admin API tokens for CI /
// script / Terraform-style automation (§4 P2, server PR #184).
// Admin-only on the wire; non-admin callers see the
// FetchErrorState surface translate the 403 into "permission
// needed" rather than rendering an empty table.
export default async function APITokensPage() {
 const tokens = await fetchAPITokens();
 if (tokens.kind !== 'ok') {
 return <FetchErrorState kind={tokens.kind} />;
 }
 return <APITokensPageView tokens={tokens.value} />;
}

function APITokensPageView({ tokens }: { tokens: APIToken[] }) {
 const t = useTranslations('apiTokens');
 return (
 <div className="space-y-6">
 <header className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between sm:gap-6">
 <div className="space-y-2">
 <h1 className="text-4xl font-light tracking-tight text-bamboo-50 sm:text-5xl">
 {t('title')}
 <span className="ml-3 font-serif italic font-normal text-bamboo-300/80">
 {t('titleAccent')}
 </span>
 </h1>
 <p className="text-sm text-bamboo-200/60">{t('subtitle')}</p>
 </div>
 </header>
 <APITokensView tokens={tokens} />
 </div>
 );
}
