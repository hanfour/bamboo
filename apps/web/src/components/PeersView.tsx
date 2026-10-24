// SPDX-License-Identifier: AGPL-3.0-or-later

'use client';

import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { useTranslations } from 'next-intl';
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
  // actions is the toolbar's right-hand slot (links and the Add peer
  // button), rendered by the server page.
  actions?: ReactNode;
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
  actions,
}: Props) {
  const t = useTranslations('peers.toolbar');
  const [filter, setFilter] = useState<'all' | 'online' | 'offline'>('all');
  const [query, setQuery] = useState('');
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const open = Boolean(selectedId);

  const pending = peers.filter((p) => p.approvalStatus === 'pending');
  const active = peers.filter((p) => p.approvalStatus === 'approved');
  // Approved exit nodes are the selectable targets for the drawer's
  // "route through exit node" picker (the consume side of #137).
  const exitNodeOptions = active.filter((p) => p.exitNodeApproved);

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

  const counts = useMemo(
    () => ({
      all: active.length,
      online: active.filter((p) => p.status === 'online').length,
      offline: active.filter((p) => p.status !== 'online').length,
    }),
    [active],
  );
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return active.filter((p) => {
      if (filter === 'online' && p.status !== 'online') return false;
      if (filter === 'offline' && p.status === 'online') return false;
      if (!q) return true;
      return [p.hostname, p.peerDnsName, p.ip, p.ip6, p.ownerEmail, p.os, ...p.tags]
        .filter(Boolean)
        .some((v) => String(v).toLowerCase().includes(q));
    });
  }, [active, filter, query]);

  return (
    <div className="space-y-6">
      {pending.length > 0 && <PendingPeersList peers={pending} />}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div role="tablist" className="flex items-center gap-1">
          {(['all', 'online', 'offline'] as const).map((key) => (
            <button
              key={key}
              type="button"
              role="tab"
              aria-selected={filter === key}
              onClick={() => setFilter(key)}
              className={[
                'h-8 rounded-full px-4 text-[13px] transition-colors',
                filter === key
                  ? 'bg-[#777]/25 text-bamboo-50'
                  : 'text-bamboo-200/60 hover:text-bamboo-50',
              ].join(' ')}
            >
              {t(key)} <span className="ml-1 text-bamboo-200/40">{counts[key]}</span>
            </button>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('search')}
            aria-label={t('search')}
            className="h-8 w-56 rounded-lg border border-[#777]/30 bg-transparent px-3 text-[13px] text-bamboo-50 outline-none placeholder:text-bamboo-200/40 focus:border-bamboo-300"
          />
          {actions}
        </div>
      </div>
      <PeerTable
        peers={shown}
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
        exitNodeOptions={exitNodeOptions}
        open={open}
        onClose={() => setSelected(null)}
        onDeleted={() => setSelected(null)}
      />
    </div>
  );
}
