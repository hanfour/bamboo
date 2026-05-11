// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { PeersView } from '@/components/PeersView';
import { fetchPeer, fetchPeerEvents, fetchPeers } from '@/lib/api';
import type { FetchResult, Peer, PeerEvent } from '@/lib/types';

type SearchParams = { selected?: string };

export default async function PeersPage({
  searchParams,
}: {
  // Next 15 makes searchParams a Promise in async server components.
  searchParams: Promise<SearchParams>;
}) {
  const { selected } = await searchParams;
  const [peers, selectedPeer, selectedEvents] = await Promise.all([
    fetchPeers(),
    selected ? fetchPeer(selected) : Promise.resolve(null),
    selected ? fetchPeerEvents(selected) : Promise.resolve([]),
  ]);
  return (
    <Peers
      peers={peers}
      selectedPeer={selectedPeer}
      selectedEvents={selectedEvents}
      selectedId={selected}
    />
  );
}

function Peers({
  peers,
  selectedPeer,
  selectedEvents,
  selectedId,
}: {
  peers: Peer[];
  selectedPeer: FetchResult<Peer> | null;
  selectedEvents: PeerEvent[];
  selectedId?: string;
}) {
  const t = useTranslations('peers');
  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
        <button
          type="button"
          className="rounded-md bg-bamboo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-bamboo-700 dark:bg-bamboo-500 dark:hover:bg-bamboo-400"
        >
          {t('addPeer')}
        </button>
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
