// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { FetchErrorState } from '@/components/FetchErrorState';
import { IngressView } from '@/components/IngressView';
import { fetchIngress, fetchMe, fetchPeers } from '@/lib/api';
import type { IngressList } from '@/lib/ingress';
import type { Peer } from '@/lib/types';

// /ingress: public services (docs/design/2026-11-02-public-services-ingress.md §7).
// Follows the relays page: server component fetches, FetchErrorState
// renders 401/403/5xx, a client view handles mutations.
export default async function IngressPage() {
  const [list, peers, me] = await Promise.all([fetchIngress(), fetchPeers(), fetchMe()]);
  if (list.kind !== 'ok') {
    return <FetchErrorState kind={list.kind} />;
  }
  const peerList = peers.kind === 'ok' ? peers.value.peers : [];
  return <IngressPageView list={list.value} peers={peerList} isAdmin={me.isAdmin === true} />;
}

function IngressPageView({ list, peers, isAdmin }: { list: IngressList; peers: Peer[]; isAdmin: boolean }) {
  const t = useTranslations('ingress');
  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">{t('title')}</h1>
        <p className="text-[13px] text-bamboo-200/60">{t('subtitle')}</p>
      </header>
      <IngressView list={list} peers={peers} isAdmin={isAdmin} />
    </div>
  );
}
