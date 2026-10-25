// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PresenceHeatmap } from './PresenceHeatmap';

vi.mock('next-intl', () => ({
  useLocale: () => 'en',
  useTranslations: () => (key: string, v?: { count?: number }) =>
    v?.count !== undefined ? `${key}:${v.count}` : key,
}));

const peers = [
  { id: 'a', hostname: 'alpha' },
  { id: 'b', hostname: 'beta' },
];
const entries = [
  { peerId: 'a', day: '2026-10-24', seconds: 3600 },
  { peerId: 'a', day: '2026-10-25', seconds: 3600 },
  { peerId: 'b', day: '2026-10-25', seconds: 1800 },
];

describe('PresenceHeatmap', () => {
  it('totals all nodes, then one node when its tab is chosen', async () => {
    render(<PresenceHeatmap entries={entries} peers={peers} today="2026-10-25" />);
    expect(screen.getByText('2.5 h')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('tab', { name: 'beta' }));
    expect(screen.getByText('30 m')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'beta' })).toHaveAttribute('aria-selected', 'true');
  });

  it('lists the busiest node first and renders empty without data', () => {
    render(<PresenceHeatmap entries={entries} peers={peers} today="2026-10-25" />);
    const names = screen.getAllByRole('tab').map((t) => t.textContent);
    expect(names).toEqual(['all', 'alpha', 'beta']);
    render(<PresenceHeatmap entries={[]} peers={[]} today="2026-10-25" />);
    expect(screen.getAllByText('0 m').length).toBeGreaterThan(0);
  });
});
