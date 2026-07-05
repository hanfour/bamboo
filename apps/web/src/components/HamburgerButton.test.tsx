// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { HamburgerButton } from './HamburgerButton';

// Smoke test that also proves the jsdom + testing-library + jest-dom
// harness works end to end. HamburgerButton reads the drawer context via
// useDrawer(), which returns the createContext default when no provider
// is mounted — so it renders standalone in the closed state.
describe('HamburgerButton', () => {
  it('renders an accessible toggle in the closed state', () => {
    render(<HamburgerButton />);
    const button = screen.getByRole('button', { name: 'Toggle navigation' });
    expect(button).toBeInTheDocument();
    expect(button).toHaveAttribute('aria-expanded', 'false');
    expect(button).toHaveAttribute('aria-controls', 'primary-sidebar');
  });

  it('draws the three-bar (closed) icon, not the X', () => {
    const { container } = render(<HamburgerButton />);
    // Closed state = three horizontal bars = three <path> elements.
    expect(container.querySelectorAll('path')).toHaveLength(3);
  });
});
