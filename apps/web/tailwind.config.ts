// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Config } from 'tailwindcss';

const config: Config = {
  content: ['./src/**/*.{ts,tsx}'],
  // `class`-opt-in dark mode. The `<html>` element in
  // src/app/[locale]/layout.tsx always carries `dark`, so the theme
  // is OS-independent. A user switching the system to Light does not
  // flip it.
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
        // Neutral greys, set 2026-10-24 to follow the Cursor dashboard
        // (page #000, surfaces #111, text #f3f3f3 / #959595 / #777).
        // The scale names stay `ink` and `bamboo` so every component
        // picks the new look up without a class rename. Use `ink` for
        // surfaces and hairlines, `bamboo` for text and controls.
        //
        // ink: 950 page (pure black), 900 sidebar and card surface, 800
        // hairline and hover fill, 700 stronger rule. Measured on the
        // Cursor dashboard: main area #000, sidebar and cards #111.
        ink: {
          50: '#F3F3F3',
          100: '#E4E4E4',
          200: '#C4C4C4',
          300: '#959595',
          400: '#777777',
          500: '#555555',
          600: '#3A3A3A',
          700: '#2E2E2E',
          800: '#222222',
          900: '#111111',
          950: '#000000',
        },
        // bamboo: 50 primary text and the light pill button, 100-200
        // secondary text (used with /60 to /80 opacity), 300-500 focus
        // rings and quiet icons, 600+ rarely used.
        bamboo: {
          50: '#F3F3F3',
          100: '#E4E4E4',
          200: '#C4C4C4',
          300: '#A8A8A8',
          400: '#8A8A8A',
          500: '#707070',
          600: '#585858',
          700: '#444444',
          800: '#333333',
          900: '#262626',
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
