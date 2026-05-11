// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { NewPeerButton } from '@/components/NewPeerButton';
import { PeersView } from '@/components/PeersView';
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
  return (
    <Peers
      peers={peers}
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
  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
        <NewPeerButton tenantSlug={tenantSlug} />
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
