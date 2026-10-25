// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeToggle } from './ThemeToggle';
import { THEME_KEY } from '@/lib/theme';

vi.mock('next-intl', () => ({ useTranslations: () => (k: string) => k }));

describe('ThemeToggle', () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.className = 'dark';
    window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} })) as never;
  });

  it('marks the stored choice and switches the page theme on click', async () => {
    window.localStorage.setItem(THEME_KEY, 'dark');
    render(<ThemeToggle />);
    expect(await screen.findByRole('radio', { name: 'dark' })).toBeChecked();

    await userEvent.click(screen.getByRole('radio', { name: 'light' }));
    expect(screen.getByRole('radio', { name: 'light' })).toBeChecked();
    expect(window.localStorage.getItem(THEME_KEY)).toBe('light');
    expect(document.documentElement.classList.contains('dark')).toBe(false);

    await userEvent.click(screen.getByRole('radio', { name: 'system' }));
    expect(document.documentElement.classList.contains('dark')).toBe(true);
  });
});
