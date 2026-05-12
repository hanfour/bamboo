// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslations } from 'next-intl';
import { NewPeerButton } from '@/components/NewPeerButton';
import { PeersView } from '@/components/PeersView';
import { FetchErrorState } from '@/components/FetchErrorState';
import { PageHeader } from '@/components/PageHeader';
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
    <div>
      <PageHeader
        kicker="peers · 節點"
        title={t('title')}
        meta={
          <>
            <Link
              href="/preauth-keys"
              className="hidden font-mono text-[11px] uppercase tracking-[0.18em] text-zinc-500 transition hover:text-zinc-200 sm:inline-block"
            >
              {tKeys('manageLink')} →
            </Link>
            <NewPeerButton tenantSlug={tenantSlug} />
          </>
        }
      />
      <PeersView
        peers={peers}
        selectedPeer={selectedPeer}
        selectedEvents={selectedEvents}
        selectedId={selectedId}
      />
    </div>
  );
}
