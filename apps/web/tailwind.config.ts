// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Config } from 'tailwindcss';

const config: Config = {
  content: ['./src/**/*.{ts,tsx}'],
  // `class`-opt-in dark mode. The `<html>` element in
  // src/app/[locale]/layout.tsx always carries `dark`, so the
  // brand voice (warm-dark) is OS-independent — a user toggling
  // their system into Light won't flip the bamboo theme out.
  darkMode: 'class',
  theme: {
    extend: {
      fontFamily: {
        // Sora as the primary humanist sans — wired via
        // next/font/google in layout.tsx, exposed through
        // --font-sans. CJK fallback chain keeps Traditional
        // Chinese legible on macOS / Windows / Linux before Sora
        // finishes loading.
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
        // Lora italic — scoped to emphasis-on-key-noun spans
        // (a la Resend's `developers`). Body text never uses
        // serif; pulling Lora onto paragraphs reverts to the
        // editorial look rejected 2026-05-12.
        serif: ['var(--font-serif)', 'ui-serif', 'Georgia', 'serif'],
        mono: ['var(--font-mono)', 'ui-monospace', 'monospace'],
        // Saira Stencil One — wordmark-only. Display fonts on
        // body text push the page back toward editorial.
        wordmark: ['var(--font-wordmark)', 'sans-serif'],
      },
      colors: {
        // bamboo as weathered / aged bamboo — wabi-sabi taupe.
        // 700+ are the warm-dark surfaces the redesigned theme
        // leans on; 50-200 act as cream highlights / body text
        // in the inverted dark palette.
        bamboo: {
          50: '#FDF6EC',
          100: '#F3EFE0',
          200: '#EADBC8',
          300: '#D9CBB0',
          400: '#C5B091',
          500: '#B59A7E',
          600: '#9D8467',
          700: '#806A52',
          800: '#5F4F3D',
          900: '#3D3327',
        },
        // ink — warm-tinted charcoal for the dark surfaces.
        // Pure zinc would clash with the warm bamboo accent;
        // these intentionally share a hue family so the
        // background and the accent feel like the same brand.
        //   950: page bg
        //   900 / 800: raised surfaces (cards, hover)
        //   700: hairlines
        //   400-500: secondary text on dark
        //   100-200: rare highlight text on a 700 surface
        ink: {
          50: '#F5F2EC',
          100: '#E8E3D8',
          200: '#C7BFAE',
          300: '#9A9183',
          400: '#6E665B',
          500: '#4E483F',
          600: '#363128',
          700: '#27221C',
          800: '#1C1814',
          900: '#13110E',
          950: '#0C0A07',
        },
        // Back-compat — older components still reference
        // stone-300 for hairlines. New code should prefer the
        // `ink` scale.
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
