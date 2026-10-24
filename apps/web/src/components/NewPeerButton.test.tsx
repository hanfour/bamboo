// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NewPeerButton } from './NewPeerButton';
import { mintPreAuthKeyAction } from '@/lib/actions';

vi.mock('next-intl', () => {
  const translate = (key: string) => key;
  return { useTranslations: () => translate };
});

vi.mock('@/lib/actions', () => ({
  mintPreAuthKeyAction: vi.fn(async () => ({
    ok: true as const,
    id: 'k1',
    description: 'db',
    secret: 'bamboo-secret',
  })),
}));

describe('NewPeerButton', () => {
  it('sends the typed tags with the new key', async () => {
    const user = userEvent.setup();
    render(<NewPeerButton tenantSlug="default" />);

    await user.click(screen.getByRole('button', { name: 'button' }));
    await user.type(screen.getByLabelText('descriptionLabel'), 'db');
    await user.type(screen.getByLabelText('tagsLabel'), 'tag:db, prod');
    await user.click(screen.getByRole('button', { name: 'submit' }));

    expect(mintPreAuthKeyAction).toHaveBeenCalledWith({
      description: 'db',
      reusable: false,
      autoApprove: false,
      tags: ['db', 'prod'],
    });
    expect(await screen.findByText('resultTitle')).toBeInTheDocument();
  });
});
