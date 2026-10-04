// SPDX-License-Identifier: AGPL-3.0-or-later

import { cookies } from 'next/headers';

// controllerProxy forwards the admin session cookie to the controller.
// Route handlers use it for desk downloads and the terminal stream,
// which do not fit a JSON server action.

export const CONTROLLER_BASE = process.env.BAMBOO_API_URL ?? 'http://localhost:8081';
const TENANT = process.env.BAMBOO_TENANT ?? 'default';
const SESSION_COOKIE = 'bamboo_session';

export async function controllerHeaders(): Promise<Record<string, string>> {
  const headers: Record<string, string> = { 'X-Tenant-Slug': TENANT };
  const store = await cookies();
  const session = store.get(SESSION_COOKIE);
  if (session?.value) {
    headers.Cookie = `${SESSION_COOKIE}=${session.value}`;
  }
  return headers;
}
