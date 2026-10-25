// SPDX-License-Identifier: AGPL-3.0-or-later

// controllerPublicUrl builds a controller URL that the *browser* follows
// (sign-in, sign-out). BAMBOO_API_URL is the in-network address the web
// server uses (http://controller:8081) and is unreachable from a browser.
// Behind the reverse proxy the controller shares the web origin, so the
// deployment sets BAMBOO_PUBLIC_API_URL to "" and the link stays relative.
// Local dev leaves it unset and talks to the controller directly.
export function controllerPublicUrl(path: string): string {
  const base = process.env.BAMBOO_PUBLIC_API_URL ?? 'http://localhost:8081';
  return `${base}${path}`;
}
