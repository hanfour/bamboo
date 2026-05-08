# web

Admin web UI for bamboo. Next.js 14 (App Router), TypeScript strict,
Tailwind CSS, `next-intl` for i18n.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

Pre-alpha scaffold. The UI renders against in-process mock data; the
gRPC-Web (or REST) bridge to the controller lands in a follow-up PR.

| Page | Route | Source of data |
| --- | --- | --- |
| Dashboard | `/[locale]` | `mockPeers` (count widgets) |
| Peers list | `/[locale]/peers` | `mockPeers` |
| ACL viewer | `/[locale]/acl` | `mockPolicy` |

## Locales

- `en` (default)
- `zh-TW` (full coverage)

URL strategy is `as-needed`: English lives at `/dashboard`, Traditional
Chinese at `/zh-TW/dashboard`. Add locales by editing
`src/i18n/routing.ts` and dropping a `messages/<locale>.json`.

## Build

```bash
cd apps/web
npm install
npm run build      # type-checks + production bundle
npm run dev        # development server on http://localhost:3000
npm run lint       # next lint
npm run typecheck  # tsc --noEmit
```

## Design

- Tailwind 3 with a single `bamboo` palette extension (subtle green).
- Dark mode via `prefers-color-scheme` with no toggle in this scaffold.
- Layout: max width 6xl (~72rem) with simple horizontal nav.
- No component library yet. shadcn/ui is the likely next step once the
  page count grows.

## Where the real data lands

The `src/lib/types.ts` shapes mirror the bamboo.v1 proto. When the
controller exposes a gRPC-Web (or REST gateway) endpoint, the mock
imports in pages get swapped for `fetch` / `useQuery` calls. We are
keeping client-side data fetching out of this PR to keep scope tight.

## Tracking

- [Sprint 2 — Issue #18](https://github.com/hanfour/bamboo/issues/18) Web UI scaffold
