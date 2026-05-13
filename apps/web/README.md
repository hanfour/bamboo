# web

Admin web UI for bamboo. Next.js 14 (App Router), TypeScript strict,
Tailwind CSS, `next-intl` for i18n.

**License:** AGPLv3 — see [LICENSE-AGPL](../../LICENSE-AGPL).

## Status

Live admin console. Server components hit the controller's
`/api/v1/*` REST surface directly (the `bamboo_session` cookie is
forwarded from the incoming Next.js request through `lib/api.ts`'s
`buildHeaders`). All page reads route through `FetchResult<T>` so the
boundary renders explicit affordances for 401 / 403 / 404 / 5xx /
network failure (`components/FetchErrorState.tsx`).

| Page | Route | Endpoint |
| --- | --- | --- |
| Dashboard | `/[locale]` | `/api/v1/overview` + `/api/v1/activity` + `/api/v1/recommendations` |
| Peers list / drawer | `/[locale]/peers` | `/api/v1/peers`, `/api/v1/peers/{id}`, `/api/v1/peers/{id}/events` |
| Users + Invitations | `/[locale]/users` | `/api/v1/users` + `/api/v1/invitations` |
| Invite landing | `/[locale]/invite` | OIDC accept flow (token from `?token=`) |
| Pre-auth keys | `/[locale]/preauth-keys` | `/api/v1/preauth-keys` |
| ACL editor | `/[locale]/acl` | `/api/v1/policy` GET + PUT |
| DNS settings | `/[locale]/dns` | `/api/v1/dns` |
| Logs | `/[locale]/logs` | `/api/v1/logs` |
| Settings | `/[locale]/settings` | `/api/v1/me` + tenant card |

Mutations live in `src/lib/actions.ts` as Server Actions:
`mintPreAuthKeyAction`, `revokePreAuthKeyAction`, `inviteUserAction`,
`revokeInvitationAction`, `setPolicyAction`, peer rename / delete /
status / tags. Each Server Action `revalidatePath`s the affected
page so the next navigation tick re-fetches.

## Locales

- `en` (default)
- `zh-TW` (full coverage; primary user-facing locale)

URL strategy is `as-needed`: English lives at `/peers`, Traditional
Chinese at `/zh-TW/peers`. Add locales by editing `src/i18n/routing.ts`
and dropping a `messages/<locale>.json`.

## Build

```bash
cd apps/web
npm install
npm run build      # type-checks + production bundle
npm run dev        # development server on http://localhost:3000
npm run lint       # next lint
npm run typecheck  # tsc --noEmit
```

Pointed at the local controller stack via env:

```bash
BAMBOO_API_URL=http://localhost:8081 BAMBOO_TENANT=default npm run dev
```

## Design

Wabi-sabi vendor-dashboard aesthetic (Cloudflare / Google / Apple
console-style), explicitly NOT editorial / serif / dark-first. See
`docs/design/2026-05-web-roadmap.md` for the design spec. Key tokens
under `tailwind.config.ts`:

- `bamboo-*` taupe palette (warm low-saturation backgrounds)
- `wire-*` hairline rule alphas
- Muji-style dot+label status vocabulary on every status badge

Chrome: BAMBOO wordmark + 7-item left sidebar (Dashboard / Peers /
Users / ACL / DNS / Logs / Settings). Hamburger drawer below `lg`.

## Auth precedence

`buildHeaders` in `src/lib/api.ts` mirrors the controller middleware:

1. `bamboo_session` cookie from the incoming Next.js request, passed
   through to the controller. Production path once OIDC sign-in
   completes.
2. `X-Tenant-Slug` header pinned to `BAMBOO_TENANT`. Dev fallback —
   the controller honors it only when `BAMBOO_REQUIRE_AUTH` is unset.

## Contract pin

`apps/controller/test/e2e/web_route_contract_test.go` runs in CI and
probes every (method, path) this app's `lib/api.ts` + `lib/actions.ts`
hits. Adding a new fetch without a matching backend route fails the
test — drift is caught before it reaches production.
