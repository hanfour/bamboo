// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { FetchErrorState } from '@/components/FetchErrorState';
import { WebhooksView } from '@/components/WebhooksView';
import { fetchWebhooks } from '@/lib/api';
import type { Webhook } from '@/lib/types';

// /webhooks lists tenant outbound webhook subscriptions (§4 P2).
// Admin-only on the wire; a non-admin browsing here sees the
// FetchErrorState surface translate the 403 into the "permission
// needed" view rather than rendering an empty table that would
// imply nothing is configured.
export default async function WebhooksPage() {
 const hooks = await fetchWebhooks();
 if (hooks.kind !== 'ok') {
 return <FetchErrorState kind={hooks.kind} />;
 }
 return <WebhooksPageView hooks={hooks.value} />;
}

function WebhooksPageView({ hooks }: { hooks: Webhook[] }) {
 const t = useTranslations('webhooks');
 return (
 <div className="space-y-6">
 <header className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between sm:gap-6">
 <div className="space-y-2">
 <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">
 {t('title')}
 </h1>
 <p className="text-sm text-bamboo-200/60">{t('subtitle')}</p>
 </div>
 </header>
 <WebhooksView hooks={hooks} />
 </div>
 );
}
