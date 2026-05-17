// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useCallback } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import type { FetchResult, Peer, PeerEvent } from '@/lib/types';
import { PeerTable } from './PeerTable';
import { PeerDrawer } from './PeerDrawer';

type Props = {
 peers: Peer[];
 selectedPeer: FetchResult<Peer> | null;
 selectedEvents: PeerEvent[];
 selectedId?: string;
};

// PeersView owns the URL transitions that open/close the drawer.
// The peer data itself is fetched server-side in peers/page.tsx so
// deep-linking to ?selected=<id> renders fully on the server; this
// component only mediates click → router.push and back.
export function PeersView({ peers, selectedPeer, selectedEvents, selectedId }: Props) {
 const router = useRouter();
 const pathname = usePathname();
 const searchParams = useSearchParams();

 const open = Boolean(selectedId);

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
 <>
 <PeerTable peers={peers} selectedId={selectedId} onSelect={(id) => setSelected(id)} />
 <PeerDrawer
 peerResult={selectedPeer}
 events={selectedEvents}
 open={open}
 onClose={() => setSelected(null)}
 onDeleted={() => setSelected(null)}
 />
 </>
 );
}
