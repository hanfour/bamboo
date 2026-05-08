import createMiddleware from 'next-intl/middleware';
import { routing } from '@/i18n/routing';

export default createMiddleware(routing);

export const config = {
  // Match every path except files with extensions and Next internals.
  matcher: ['/((?!api|_next|.*\\..*).*)'],
};
