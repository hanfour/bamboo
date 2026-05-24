// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { FetchErrorState } from '@/components/FetchErrorState';
import { RelaysTable } from '@/components/RelaysTable';
import { fetchRelays } from '@/lib/api';
import type { Relay } from '@/lib/api';

// /relays is the super-admin relay registry surface (§4 P2 multi-
// relay registry, stage 3). Read-only in v1: operators add / soft-
// delete relays via the REST API directly until the create form
// ships in a follow-up. The page's primary value today is making
// the §4 P2 stage 1 health badges visible — "which of my relays
// are reachable RIGHT NOW".
//
// Admin-only on the wire; non-admin callers get a 403 which the
// FetchErrorState surface translates into the "permission needed"
// view rather than rendering an empty table that would imply
// nothing is configured.
export default async function RelaysPage() {
  const relays = await fetchRelays();
  if (relays.kind !== 'ok') {
    return <FetchErrorState kind={relays.kind} />;
  }
  return <RelaysPageView relays={relays.value} />;
}

function RelaysPageView({ relays }: { relays: Relay[] }) {
  const t = useTranslations('relays');
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
      <RelaysTable relays={relays} />
    </div>
  );
}
