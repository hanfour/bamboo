// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it } from 'vitest';
import { THEME_KEY, readTheme, resolveDark, saveTheme, themeInitScript } from './theme';

describe('resolveDark', () => {
  it('follows the OS only for "system"', () => {
    expect(resolveDark('system', true)).toBe(true);
    expect(resolveDark('system', false)).toBe(false);
    expect(resolveDark('light', true)).toBe(false);
    expect(resolveDark('dark', false)).toBe(true);
  });
});

describe('stored theme', () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.className = 'dark';
    window.matchMedia = ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} })) as never;
  });

  it('defaults to system and ignores junk', () => {
    expect(readTheme()).toBe('system');
    window.localStorage.setItem(THEME_KEY, 'purple');
    expect(readTheme()).toBe('system');
  });

  it('saves the choice and flips the html class', () => {
    saveTheme('light');
    expect(readTheme()).toBe('light');
    expect(document.documentElement.classList.contains('dark')).toBe(false);
    saveTheme('dark');
    expect(document.documentElement.classList.contains('dark')).toBe(true);
  });

  it('init script agrees with applyTheme for a stored light choice', () => {
    window.localStorage.setItem(THEME_KEY, 'light');
    document.documentElement.className = 'dark';
    new Function(themeInitScript)();
    expect(document.documentElement.classList.contains('dark')).toBe(false);
  });
});
