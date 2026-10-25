// SPDX-License-Identifier: AGPL-3.0-or-later

// Theme preference. "system" follows the OS; the choice is stored in
// localStorage and resolved to a `dark` class on <html>. The class drives
// both the `dark:` variants and the color variables in globals.css.

export type Theme = 'system' | 'light' | 'dark';

export const THEME_KEY = 'bamboo-theme';

export function isTheme(v: unknown): v is Theme {
  return v === 'system' || v === 'light' || v === 'dark';
}

export function readTheme(): Theme {
  try {
    const v = window.localStorage.getItem(THEME_KEY);
    return isTheme(v) ? v : 'system';
  } catch {
    return 'system';
  }
}

export function resolveDark(theme: Theme, prefersDark: boolean): boolean {
  return theme === 'dark' || (theme === 'system' && prefersDark);
}

export function applyTheme(theme: Theme): void {
  const dark = resolveDark(theme, window.matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.classList.toggle('dark', dark);
}

export function saveTheme(theme: Theme): void {
  try {
    window.localStorage.setItem(THEME_KEY, theme);
  } catch {
    // Private mode or blocked storage: the choice lasts for this page only.
  }
  applyTheme(theme);
}

// themeInitScript runs in <head> before first paint so a stored light
// choice never flashes dark. It mirrors readTheme and applyTheme and must
// stay dependency-free. With nothing stored, "system" applies. The server
// renders `dark` as the default class (the original look) and this script
// corrects it.
export const themeInitScript = `(function(){try{var t=localStorage.getItem('${THEME_KEY}');var d=t==='dark'||((t!=='light')&&window.matchMedia('(prefers-color-scheme: dark)').matches);document.documentElement.classList.toggle('dark',d)}catch(e){}})()`;
