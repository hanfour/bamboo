import type { Config } from 'tailwindcss';

const config: Config = {
  content: ['./src/**/*.{ts,tsx}'],
  darkMode: 'media',
  theme: {
    extend: {
      fontFamily: {
        sans: ['var(--font-sans)', 'system-ui', 'sans-serif'],
        mono: ['var(--font-mono)', 'ui-monospace', 'monospace'],
      },
      colors: {
        // Subtle bamboo green; tuned for both light and dark backgrounds.
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
    },
  },
  plugins: [],
};

export default config;
