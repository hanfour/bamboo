// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Config } from 'tailwindcss';

const config: Config = {
  content: ['./src/**/*.{ts,tsx}'],
  // `class`-opt-in dark mode. `<html>` carries `dark` when the resolved
  // theme is dark (see src/lib/theme.ts). `dark:` variants and the
  // color variables in globals.css both key off it.
  darkMode: 'class',
  theme: {
    extend: {
      fontFamily: {
        // One neutral sans for everything, wired via next/font in
        // layout.tsx as --font-sans. The CJK chain keeps Traditional
        // Chinese legible before the web font loads.
        sans: [
          'var(--font-sans)',
          'ui-sans-serif',
          'system-ui',
          '-apple-system',
          'BlinkMacSystemFont',
          '"PingFang TC"',
          '"Noto Sans TC"',
          '"Hiragino Sans"',
          '"Microsoft JhengHei"',
          'sans-serif',
        ],
        mono: ['var(--font-mono)', 'ui-monospace', 'monospace'],
      },
      colors: {
        // Neutral greys. Values live in src/app/globals.css as RGB triples
        // (light on :root, dark under html.dark) so the theme can switch
        // at runtime. `ink` is for surfaces and hairlines: 950 page, 900
        // sidebar and cards, 850 raised hover, 800 hairline and fill. `bamboo`
        // is for text and controls: 50 primary text and the pill button
        // fill, 100-200 secondary text (used with /60 to /80 opacity).
        ink: {
          50: 'rgb(var(--ink-50) / <alpha-value>)',
          100: 'rgb(var(--ink-100) / <alpha-value>)',
          200: 'rgb(var(--ink-200) / <alpha-value>)',
          300: 'rgb(var(--ink-300) / <alpha-value>)',
          400: 'rgb(var(--ink-400) / <alpha-value>)',
          500: 'rgb(var(--ink-500) / <alpha-value>)',
          600: 'rgb(var(--ink-600) / <alpha-value>)',
          700: 'rgb(var(--ink-700) / <alpha-value>)',
          800: 'rgb(var(--ink-800) / <alpha-value>)',
          850: 'rgb(var(--ink-850) / <alpha-value>)',
          900: 'rgb(var(--ink-900) / <alpha-value>)',
          950: 'rgb(var(--ink-950) / <alpha-value>)',
        },
        bamboo: {
          50: 'rgb(var(--bamboo-50) / <alpha-value>)',
          100: 'rgb(var(--bamboo-100) / <alpha-value>)',
          200: 'rgb(var(--bamboo-200) / <alpha-value>)',
          300: 'rgb(var(--bamboo-300) / <alpha-value>)',
          400: 'rgb(var(--bamboo-400) / <alpha-value>)',
          500: 'rgb(var(--bamboo-500) / <alpha-value>)',
          600: 'rgb(var(--bamboo-600) / <alpha-value>)',
          700: 'rgb(var(--bamboo-700) / <alpha-value>)',
          800: 'rgb(var(--bamboo-800) / <alpha-value>)',
          900: 'rgb(var(--bamboo-900) / <alpha-value>)',
        },
        // Back-compat for older hairlines that name stone-300.
        stone: {
          300: '#D3D3D3',
        },
      },
      // Heading line-heights: Tailwind's defaults are too tight
      // for the bigger 5xl / 6xl heads we render with
      // `font-light tracking-tight`. CJK descenders need a hair
      // more room than Latin-only would suggest.
      fontSize: {
        '5xl': ['3rem', { lineHeight: '1.1' }],
        '6xl': ['3.75rem', { lineHeight: '1.05' }],
      },
    },
  },
  plugins: [],
};

export default config;
