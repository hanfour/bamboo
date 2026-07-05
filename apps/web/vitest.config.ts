// SPDX-License-Identifier: AGPL-3.0-or-later

import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

// Vitest config for the web admin UI. No @vitejs/plugin-react (its vite
// peer range conflicts with the vitest 2 bundle); esbuild's automatic
// JSX runtime is enough to transform .tsx for component tests. jsdom is
// the default environment so component tests can render; pure lib tests
// run fine there too.
export default defineConfig({
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  esbuild: {
    jsx: 'automatic',
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    css: false,
  },
});
