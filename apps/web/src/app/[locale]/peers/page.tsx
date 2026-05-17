// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { NewPeerButton } from '@/components/NewPeerButton';
import { PeersView } from '@/components/PeersView';
import { FetchErrorState } from '@/components/FetchErrorState';
import { Link } from '@/i18n/routing';
import { fetchMe, fetchPeer, fetchPeerEvents, fetchPeers } from '@/lib/api';
import type { FetchResult, Peer, PeerEvent } from '@/lib/types';

type SearchParams = { selected?: string };

export default async function PeersPage({
 searchParams,
}: {
 // Next 15 makes searchParams a Promise in async server components.
 searchParams: Promise<SearchParams>;
}) {
 const { selected } = await searchParams;
 const [peers, selectedPeer, selectedEvents, me] = await Promise.all([
 fetchPeers(),
 selected ? fetchPeer(selected) : Promise.resolve(null),
 selected ? fetchPeerEvents(selected) : Promise.resolve([]),
 fetchMe(),
 ]);

 // The peer table is the page's primary purpose. If we can't load it
 // we surface the auth / network state instead of an empty table that
 // would lie about there being no peers. The selectedPeer drawer state
 // already handles its own variants downstream (see PeerDrawer).
 if (peers.kind !== 'ok') {
 return <FetchErrorState kind={peers.kind} />;
 }

 return (
 <Peers
 peers={peers.value}
 selectedPeer={selectedPeer}
 selectedEvents={selectedEvents}
 selectedId={selected}
 tenantSlug={me.tenantSlug}
 />
 );
}

function Peers({
 peers,
 selectedPeer,
 selectedEvents,
 selectedId,
 tenantSlug,
}: {
 peers: Peer[];
 selectedPeer: FetchResult<Peer> | null;
 selectedEvents: PeerEvent[];
 selectedId?: string;
 tenantSlug: string;
}) {
 const t = useTranslations('peers');
 const tKeys = useTranslations('preAuthKeys');
 return (
 <div className="space-y-8 pt-2">
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
 <div className="flex items-center gap-4">
 <Link
 href="/preauth-keys"
 className="text-xs text-bamboo-200/70 transition-colors hover:text-bamboo-50"
 >
 {tKeys('manageLink')}
 </Link>
 <NewPeerButton tenantSlug={tenantSlug} />
 </div>
 </header>
 <PeersView
 peers={peers}
 selectedPeer={selectedPeer}
 selectedEvents={selectedEvents}
 selectedId={selectedId}
 />
 </div>
 );
}
