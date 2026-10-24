// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { NewPeerButton } from '@/components/NewPeerButton';
import { PeersView } from '@/components/PeersView';
import { FetchErrorState } from '@/components/FetchErrorState';
import { Link } from '@/i18n/routing';
import {
 fetchMe,
 fetchPeer,
 fetchPeerBandwidth,
 fetchPeerConnectionEvents,
 fetchPeerEvents,
 fetchPeerRouteConflicts,
 fetchPeers,
 type PeerBandwidthSample,
 type PeerConnectionEvent,
 type PeerRouteConflict,
} from '@/lib/api';
import type { FetchResult, Peer, PeerEvent } from '@/lib/types';

type SearchParams = { selected?: string };

export default async function PeersPage({
 searchParams,
}: {
 // Next 15 makes searchParams a Promise in async server components.
 searchParams: Promise<SearchParams>;
}) {
 const { selected } = await searchParams;
 const [
 peers,
 selectedPeer,
 selectedEvents,
 selectedConnectionEvents,
 selectedRouteConflicts,
 selectedBandwidth,
 me,
 ] = await Promise.all([
 fetchPeers(),
 selected ? fetchPeer(selected) : Promise.resolve(null),
 selected ? fetchPeerEvents(selected) : Promise.resolve([]),
 selected ? fetchPeerConnectionEvents(selected) : Promise.resolve([] as PeerConnectionEvent[]),
 selected ? fetchPeerRouteConflicts(selected) : Promise.resolve([] as PeerRouteConflict[]),
 selected ? fetchPeerBandwidth(selected) : Promise.resolve([] as PeerBandwidthSample[]),
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
 peers={peers.value.peers}
 latestClientVersion={peers.value.latestClientVersion}
 selectedPeer={selectedPeer}
 selectedEvents={selectedEvents}
 selectedConnectionEvents={selectedConnectionEvents}
 selectedRouteConflicts={selectedRouteConflicts}
 selectedBandwidth={selectedBandwidth}
 selectedId={selected}
 tenantSlug={me.tenantSlug}
 />
 );
}

function Peers({
 peers,
 latestClientVersion,
 selectedPeer,
 selectedEvents,
 selectedConnectionEvents,
 selectedRouteConflicts,
 selectedBandwidth,
 selectedId,
 tenantSlug,
}: {
 peers: Peer[];
 latestClientVersion?: string;
 selectedPeer: FetchResult<Peer> | null;
 selectedEvents: PeerEvent[];
 selectedConnectionEvents: PeerConnectionEvent[];
 selectedRouteConflicts: PeerRouteConflict[];
 selectedBandwidth: PeerBandwidthSample[];
 selectedId?: string;
 tenantSlug: string;
}) {
 const t = useTranslations('peers');
 const tKeys = useTranslations('preAuthKeys');
 return (
 <div className="space-y-6">
 <header className="space-y-1.5">
 <h1 className="text-2xl font-medium tracking-tight text-bamboo-50">{t('title')}</h1>
 <p className="text-[13px] text-bamboo-200/60">{t('subtitle')}</p>
 </header>
 <PeersView
 actions={
 <>
 <Link
 href="/preauth-keys"
 className="inline-flex h-8 items-center rounded-full bg-[#777]/20 px-4 text-[13px] font-medium text-bamboo-50 transition-colors hover:bg-[#777]/30"
 >
 {tKeys('manageLink')}
 </Link>
 <NewPeerButton tenantSlug={tenantSlug} />
 </>
 }
 peers={peers}
 latestClientVersion={latestClientVersion}
 selectedPeer={selectedPeer}
 selectedEvents={selectedEvents}
 selectedConnectionEvents={selectedConnectionEvents}
 selectedRouteConflicts={selectedRouteConflicts}
 selectedBandwidth={selectedBandwidth}
 selectedId={selectedId}
 />
 </div>
 );
}
