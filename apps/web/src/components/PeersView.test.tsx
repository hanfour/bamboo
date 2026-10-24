// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PeersView } from './PeersView';
import type { Peer } from '@/lib/types';

vi.mock('next-intl', () => {
  const translate = (key: string) => key;
  return { useTranslations: () => translate };
});

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn() }),
  usePathname: () => '/peers',
  useSearchParams: () => new URLSearchParams(),
}));

// The table and drawer have their own tests and heavy dependencies. A
// stub that lists hostnames is enough to see what the toolbar filters.
vi.mock('./PeerTable', () => ({
  PeerTable: ({ peers }: { peers: Peer[] }) => (
    <ul>
      {peers.map((p) => (
        <li key={p.id}>{p.hostname}</li>
      ))}
    </ul>
  ),
}));
vi.mock('./PeerDrawer', () => ({ PeerDrawer: () => null }));
vi.mock('./PendingPeersList', () => ({ PendingPeersList: () => null }));

function peer(id: string, hostname: string, status: Peer['status'], tags: string[] = []): Peer {
  return {
    id,
    tenantId: 't',
    hostname,
    ip: `100.64.0.${id}`,
    tags,
    os: 'linux',
    clientVersion: '0.1.14',
    status,
    endpoints: [],
    rxBytes: 0,
    txBytes: 0,
    createdAt: '2026-01-01T00:00:00Z',
    approvalStatus: 'approved',
  } as Peer;
}

const peers = [
  peer('1', 'mac-mini', 'online', ['dev']),
  peer('2', 'egress', 'online', ['db', 'prod']),
  peer('3', 'h4', 'offline'),
];

function renderView() {
  render(
    <PeersView
      peers={peers}
      selectedPeer={null}
      selectedEvents={[]}
      selectedConnectionEvents={[]}
      selectedRouteConflicts={[]}
      selectedBandwidth={[]}
      actions={<button type="button">add</button>}
    />,
  );
}

describe('PeersView toolbar', () => {
  it('shows every peer and the counts per tab', () => {
    renderView();
    expect(screen.getAllByRole('listitem')).toHaveLength(3);
    expect(screen.getByRole('tab', { name: /all/ })).toHaveTextContent('3');
    expect(screen.getByRole('tab', { name: /online/ })).toHaveTextContent('2');
    expect(screen.getByRole('tab', { name: /offline/ })).toHaveTextContent('1');
    expect(screen.getByRole('button', { name: 'add' })).toBeInTheDocument();
  });

  it('filters by the offline tab', async () => {
    const user = userEvent.setup();
    renderView();
    await user.click(screen.getByRole('tab', { name: /offline/ }));
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(['h4']);
  });

  it('searches hostnames, addresses and tags', async () => {
    const user = userEvent.setup();
    renderView();
    const box = screen.getByRole('searchbox');
    await user.type(box, 'prod');
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(['egress']);
    await user.clear(box);
    await user.type(box, '100.64.0.1');
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(['mac-mini']);
  });
});
