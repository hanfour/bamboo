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
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
        <div className="flex items-center gap-3">
          <Link
            href="/preauth-keys"
            className="text-xs text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
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
