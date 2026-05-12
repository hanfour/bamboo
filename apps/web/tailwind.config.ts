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
        // bamboo as weathered / aged bamboo — wabi-sabi taupe palette.
        // Conceptual shift from "fresh green bamboo" to "seasoned bamboo
        // brown"; the token name keeps the brand association while the
        // hue moves to warm earth tones. 70% main bg (50) + 25% surfaces
        // (100-300) + 5% accent (500) distribution is the design intent.
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
        // Warm utility neutral for hairlines / dividers — pairs with
        // the bamboo earth tones better than zinc's cool grey.
        stone: {
          300: '#D3D3D3',
        },
      },
    },
  },
  plugins: [],
};

export default config;
