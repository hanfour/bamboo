import createNextIntlPlugin from 'next-intl/plugin';

const withNextIntl = createNextIntlPlugin('./src/i18n/request.ts');

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Static export is not appropriate here (we expect to call the controller
  // from the server). Server-rendered by default.
  poweredByHeader: false,
};

export default withNextIntl(nextConfig);
