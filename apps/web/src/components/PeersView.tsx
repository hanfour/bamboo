// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useCallback } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import type { PeerBandwidthSample, PeerConnectionEvent, PeerRouteConflict } from '@/lib/api';
import type { FetchResult, Peer, PeerEvent } from '@/lib/types';
import { PeerTable } from './PeerTable';
import { PeerDrawer } from './PeerDrawer';
import { PendingPeersList } from './PendingPeersList';

type Props = {
  peers: Peer[];
  // latestClientVersion: tenant's controller view of the newest
  // stable client release, surfaced once at the page level so the
  // PeerTable cell can compare every row against the same value.
  // Absent when the controller's release-feed poller is disabled or
  // stale; the cell treats both as "no badge".
  latestClientVersion?: string;
  selectedPeer: FetchResult<Peer> | null;
  selectedEvents: PeerEvent[];
  selectedConnectionEvents: PeerConnectionEvent[];
  selectedRouteConflicts: PeerRouteConflict[];
  selectedBandwidth: PeerBandwidthSample[];
  selectedId?: string;
};

// PeersView owns the URL transitions that open/close the drawer.
// The peer data itself is fetched server-side in peers/page.tsx so
// deep-linking to ?selected=<id> renders fully on the server; this
// component only mediates click → router.push and back.
//
// Pending-peers split (issue #133): rows with approvalStatus='pending'
// are pulled out into PendingPeersList above the main table. Rejected
// peers are filtered out of the active table — the audit trail keeps
// the row, but it doesn't belong in the active-membership view.
export function PeersView({
  peers,
  latestClientVersion,
  selectedPeer,
  selectedEvents,
  selectedConnectionEvents,
  selectedRouteConflicts,
  selectedBandwidth,
  selectedId,
}: Props) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const open = Boolean(selectedId);

  const pending = peers.filter((p) => p.approvalStatus === 'pending');
  const active = peers.filter((p) => p.approvalStatus === 'approved');

  const setSelected = useCallback(
    (id: string | null) => {
      const params = new URLSearchParams(searchParams.toString());
      if (id) {
        params.set('selected', id);
      } else {
        params.delete('selected');
      }
      const query = params.toString();
      router.push(query ? `${pathname}?${query}` : pathname, { scroll: false });
    },
    [pathname, router, searchParams],
  );

  return (
    <div className="space-y-8">
      {pending.length > 0 && <PendingPeersList peers={pending} />}
      <PeerTable
        peers={active}
        latestClientVersion={latestClientVersion}
        selectedId={selectedId}
        onSelect={(id) => setSelected(id)}
      />
      <PeerDrawer
        peerResult={selectedPeer}
        events={selectedEvents}
        connectionEvents={selectedConnectionEvents}
        routeConflicts={selectedRouteConflicts}
        bandwidth={selectedBandwidth}
        open={open}
        onClose={() => setSelected(null)}
        onDeleted={() => setSelected(null)}
      />
    </div>
  );
}
