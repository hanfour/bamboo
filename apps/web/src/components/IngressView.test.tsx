// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { IngressView } from './IngressView';
import {
  approveIngressAction,
  createIngressAction,
  deleteIngressAction,
  issueIngressTokenAction,
} from '@/lib/ingressActions';
import type { IngressList } from '@/lib/ingress';
import type { Peer } from '@/lib/types';

vi.mock('next-intl', () => {
  const translate = (key: string) => key;
  return { useTranslations: () => translate };
});

vi.mock('@/lib/ingressActions', () => ({
  createIngressAction: vi.fn(async () => ({ ok: true as const })),
  approveIngressAction: vi.fn(async () => ({ ok: true as const })),
  setIngressEnabledAction: vi.fn(async () => ({ ok: true as const })),
  deleteIngressAction: vi.fn(async () => ({ ok: true as const })),
  issueIngressTokenAction: vi.fn(async () => ({ ok: true as const, token: 'tok-secret', expiresAt: '2027-11-02T00:00:00Z' })),
}));

const peers = [
  { id: 'p1', hostname: 'mac', ip: '100.64.0.5', tags: [], approvalStatus: 'approved' },
  { id: 'p2', hostname: 'vps', ip: '100.64.0.9', tags: ['ingress'], approvalStatus: 'approved' },
] as unknown as Peer[];

const list: IngressList = {
  suffix: 'svc.miilink.net',
  token: null,
  services: [{
    id: 's1', peerId: 'p1', peerHostname: 'mac', peerIp: '100.64.0.5', label: 'yeschef',
    url: 'https://yeschef.svc.miilink.net', targetPort: 47800, enabled: false, approved: false,
    live: false, createdAt: '2026-11-02T00:00:00Z', updatedAt: '2026-11-02T00:00:00Z',
  }],
};

describe('IngressView', () => {
  beforeEach(() => vi.clearAllMocks());

  it('shows a pending service and lets an admin approve it', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    expect(screen.getByText('https://yeschef.svc.miilink.net')).toBeInTheDocument();
    expect(screen.getByText('state.pending')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'actions.approve' }));
    expect(approveIngressAction).toHaveBeenCalledWith('s1');
  });

  it('hides admin actions from members', () => {
    render(<IngressView list={list} peers={peers} isAdmin={false} />);
    expect(screen.queryByRole('button', { name: 'actions.approve' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'token.issue' })).toBeNull();
  });

  it('validates the label before creating', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    await user.click(screen.getByRole('button', { name: 'form.openButton' }));
    await user.selectOptions(screen.getByLabelText('form.peerLabel'), 'p1');
    await user.type(screen.getByLabelText('form.labelLabel'), 'Bad_Label');
    await user.clear(screen.getByLabelText('form.portLabel'));
    await user.type(screen.getByLabelText('form.portLabel'), '47800');
    await user.click(screen.getByRole('button', { name: 'form.submit' }));
    expect(createIngressAction).not.toHaveBeenCalled();
    expect(screen.getByText('form.invalidLabel')).toBeInTheDocument();

    await user.clear(screen.getByLabelText('form.labelLabel'));
    await user.type(screen.getByLabelText('form.labelLabel'), 'notes');
    await user.click(screen.getByRole('button', { name: 'form.submit' }));
    expect(createIngressAction).toHaveBeenCalledWith('p1', 'notes', 47800);
  });

  it('issues a token for an ingress-tagged peer and shows it once', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    await user.selectOptions(screen.getByLabelText('token.peerLabel'), 'p2');
    await user.click(screen.getByRole('button', { name: 'token.issue' }));
    expect(issueIngressTokenAction).toHaveBeenCalledWith('p2');
    expect(await screen.findByText('tok-secret')).toBeInTheDocument();
  });

  it('asks for confirmation before deleting', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    await user.click(screen.getByRole('button', { name: 'actions.delete' }));
    expect(deleteIngressAction).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'actions.cancel' }));
    expect(deleteIngressAction).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'actions.delete' }));
    await user.click(screen.getByRole('button', { name: 'actions.confirmDelete' }));
    expect(deleteIngressAction).toHaveBeenCalledWith('s1');
  });

  it('shows the controller error when an action is refused', async () => {
    vi.mocked(approveIngressAction).mockResolvedValueOnce({ ok: false, error: '403 admin required' });
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    await user.click(screen.getByRole('button', { name: 'actions.approve' }));
    expect(await screen.findByText(/403 admin required/)).toBeInTheDocument();
  });

  it('drops the revealed token once dismissed', async () => {
    const user = userEvent.setup();
    render(<IngressView list={list} peers={peers} isAdmin />);
    await user.selectOptions(screen.getByLabelText('token.peerLabel'), 'p2');
    await user.click(screen.getByRole('button', { name: 'token.issue' }));
    expect(await screen.findByText('tok-secret')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'token.close' }));
    expect(screen.queryByText('tok-secret')).toBeNull();
  });
});
