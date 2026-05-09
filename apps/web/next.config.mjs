import createNextIntlPlugin from 'next-intl/plugin';

const withNextIntl = createNextIntlPlugin('./src/i18n/request.ts');

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Static export is not appropriate here (we expect to call the controller
  // from the server). Server-rendered by default.
  poweredByHeader: false,
  // standalone bundles only what the server needs into .next/standalone,
  // so the production Docker image stays small (~150 MB vs ~600 MB).
  output: 'standalone',
};

export default withNextIntl(nextConfig);
