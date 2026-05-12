import type { Config } from 'tailwindcss';

// Design tokens for the bamboo Web. The aesthetic is "network
// operations console / engineering field manual": warm near-black
// surfaces, hairline rules, a single muted bamboo-green accent, and
// a typographic system that pairs editorial serif headings with
// technical mono readouts. We avoid neon and gradient noise — every
// emphasis is paid for in restraint.
const config: Config = {
  content: ['./src/**/*.{ts,tsx}'],
  darkMode: 'media',
  theme: {
    extend: {
      fontFamily: {
        // Body UI — DM Sans variable, low-contrast humanist sans.
        sans: ['var(--font-sans)', 'system-ui', 'sans-serif'],
        // Numbers, code, technical labels — DM Mono.
        mono: ['var(--font-mono)', 'ui-monospace', 'monospace'],
        // Page hero / editorial — Newsreader variable serif.
        serif: ['var(--font-serif)', 'ui-serif', 'Georgia', 'serif'],
      },
      colors: {
        // Warm near-black surfaces — a touch warmer than zinc-950
        // so the eye reads "ink" instead of "raw OLED black". Used
        // for the body background and the hero composition.
        ink: {
          950: '#0a0a0c',
          900: '#101013',
          800: '#16161a',
          700: '#1f1f24',
        },
        // Hairline rules — used as borders on cards, dividers, table
        // rows. Always paired with `border-` utilities so the alpha
        // stays consistent.
        wire: {
          DEFAULT: 'rgb(255 255 255 / 0.06)',
          strong: 'rgb(255 255 255 / 0.12)',
          accent: 'rgb(143 198 162 / 0.20)',
        },
        // Subtle bamboo green; tuned for both light and dark backgrounds.
        // Kept compatible with previous usage (StatusBadge, ACL rule
        // chip, drawer focus ring) — values unchanged from v1.
        bamboo: {
          50: '#f1f8f3',
          100: '#deeee2',
          200: '#bedcc7',
          300: '#90c2a2',
          400: '#5fa078',
          500: '#3d845b',
          600: '#2d6948',
          700: '#25543b',
          800: '#1f4331',
          900: '#1a3729',
        },
      },
      letterSpacing: {
        // Editorial: bigger negative tracking on serif headings,
        // restrained positive tracking on mono kickers.
        display: '-0.02em',
        kicker: '0.18em',
      },
      boxShadow: {
        // Used sparingly: only on the elevated hero card.
        elevated:
          '0 0 0 1px rgb(255 255 255 / 0.04), 0 30px 60px -30px rgb(0 0 0 / 0.6)',
      },
      animation: {
        // One staggered fade-up used on the unauthenticated hero so
        // it doesn't feel static. Earned, not gratuitous.
        'fade-up': 'fade-up 600ms cubic-bezier(0.16, 1, 0.3, 1) both',
      },
      keyframes: {
        'fade-up': {
          '0%': { opacity: '0', transform: 'translateY(8px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
      },
    },
  },
  plugins: [],
};

export default config;
