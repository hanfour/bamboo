# Web design + roadmap (2026-05-12)

This document captures the visual direction and the feature roadmap for `apps/web`
agreed during the Muji + wabi-sabi rebuild sprint in mid-May 2026.

It is **not** a static design system spec — it is the working alignment that
shipped in PRs #69 through #73 and the open backlog that follows.

## 1. Context

By 2026-05-12 the app shipped:

- v0.1.7 with Google OAuth (`BAMBOO_REQUIRE_AUTH=true`) live on `bamboo.miilink.net`
- A first chrome iteration (PR #69) — horizontal 4-tab top bar, Saira Stencil
  wordmark, neutral zinc body, bamboo-500 green accent
- A four-PR Muji surface sweep (#70-#72) — outline icons, dot-status, hairline
  borders, single-accent discipline
- Two visual identity decisions in PR #73:
  - **Palette pivot**: bamboo green → seasoned-bamboo taupe (wabi-sabi)
  - **Chrome pivot**: horizontal tabs → Google-Account-style left sidebar

We rejected (on the same day) an editorial / Newsreader-serif direction
attempted on `archive/web-design-elevation-2026-05-12` and a Tailscale-clone
horizontal-tab nav direction proposed mid-sprint.

## 2. Visual direction

### 2.1 Palette — wabi-sabi taupe

bamboo brand stays bamboo. The conceptual move is from **fresh green bamboo
→ weathered, seasoned bamboo**. Same plant, different season. Brand identity
is unchanged; only the hue rotates.

Token stops (Tailwind, `bamboo-*`):

| Token | Hex | Role |
|---|---|---|
| `bamboo-50` | `#FDF6EC` | Page main bg (70% surface) |
| `bamboo-100` | `#F3EFE0` | Cream surfaces / hover / active-pill bg |
| `bamboo-200` | `#EADBC8` | Cards / outline tints |
| `bamboo-300` | `#D9CBB0` | Hairline accent / outlined pill borders |
| `bamboo-400` | `#C5B091` | Mid-taupe transition |
| `bamboo-500` | `#B59A7E` | **Single brand accent — taupe** (5%) |
| `bamboo-600` | `#9D8467` | Accent hover / pressed |
| `bamboo-700` | `#806A52` | Accent text on light bg / active-pill text |
| `bamboo-800` | `#5F4F3D` | Deep |
| `bamboo-900` | `#3D3327` | Text-heavy / future dark-mode bg |

Plus `stone-300: #D3D3D3` for utility neutral hairlines (pairs with the
earth tones better than zinc's cool grey).

Distribution intent: **70% main bg + 25% body / surfaces + 5% accent**.

Where the accent goes:

- Active nav pill (`bamboo-100` fill, `bamboo-700` text + icon)
- Selected peer row 2px left-edge stripe
- StatusBadge "alive" dots (peer online, key reusable)
- `peer.update` audit row "to" diff badge

Body text stays `zinc-900` for legibility against cream; using `bamboo-900`
would still hit AAA contrast but reads softer than utility text needs.

### 2.2 Chrome — Google Account / Workspace Admin layout

| Surface | Spec |
|---|---|
| Top bar | `h-14`, sticky, `bg-white/95 backdrop-blur`, hairline bottom border. Brand wordmark left + user pill right. **No nav.** |
| Sidebar (lg+) | `w-60`, sticky below top bar, transparent bg (continues page cream), `border-r border-zinc-200` |
| Sidebar (<lg) | Fixed slide-in drawer, `bg-bamboo-50`, backdrop overlay, `translate-x` toggle |
| Active nav item | `bg-bamboo-100` pill + `text-bamboo-700` + icon in same taupe |
| Hover (inactive) | `bg-bamboo-50` pill + `text-zinc-900` |
| Inactive | `text-zinc-700` + icon `text-zinc-500` |
| Primary CTA | `bg-zinc-900` solid (not bamboo — keeps bamboo reserved for state) |
| Destructive | Outlined red-300 / red-700 text. **Never solid red fill.** |

Breakpoint: lg (1024px). Below lg the sidebar becomes a drawer toggled by
HamburgerButton in the TopBar. Drawer auto-closes on route change, ESC, or
backdrop click. Body scroll locks while open.

### 2.3 Surface treatment — Muji discipline

Inherited from the four-PR sweep (#70-#72):

- **Pure flat colors only** — no gradients, no tints-of-tints, no shadows
- **Outline icons only** — no filled glyphs, no duotone, no decorative iconography
- **Single accent** — `bamboo-500` reserved for state signaling, never for chrome
- **No logo glyph** — wordmark-only ("bamboo" in Saira Stencil One via
  `--font-wordmark` CSS variable scoped to the brand element)
- **Restraint over decoration** — if it doesn't communicate state or hierarchy,
  drop it

`TopBar.tsx` + `Sidebar.tsx` (post-PR #73) are the canonical reference for
what "done" looks like.

### 2.4 Reference matrix

Different concerns reach for different references — keep them in their lane:

| Layer | Reference | Notes |
|---|---|---|
| Palette / mood | wabi-sabi / 無印良品 / aged bamboo | User's explicit aesthetic — warm, restrained, earthy |
| Chrome (nav + layout) | Google Account / Workspace Admin / GCP Console | Sidebar + minimal top bar. **Deliberately different from Tailscale's horizontal nav** |
| Surface treatment | Muji / Cloudflare | Flat colors, outlined badges, hairline borders, no filled tints |
| Table + page IA | Tailscale | Search + filters + count chip + kebab row actions + dot status |
| Feature scope | Tailscale | Machines / Users / Access / DNS / Logs / Settings as the IA target |

### 2.5 Rejected directions (do not re-attempt without explicit OK)

- **Editorial / Newsreader-serif / dark-first** — tried 2026-05-12 on
  `archive/web-design-elevation-2026-05-12` (commit `1c2d116`). Read as
  "designer's portfolio" rather than vendor-grade SaaS.
- **Horizontal top-tab nav (any count)** — too Tailscale-like. The current
  sidebar chrome is the agreed alternative.
- **Generic "vendor-dashboard" framing** — too vague. Use the four-layer
  reference matrix above instead.
- **Green bamboo palette** — replaced by taupe as of PR #73. Do not revert.
- **Logo glyph experiments** — multiple drafts (Mahjong 二條, 3×3 nine-grid,
  elongated pills) all rejected as competitor-adjacent or decorative noise.
  Wordmark-only is the agreed final state.

## 3. Functional roadmap — Tailscale-inspired

### 3.1 IA gap matrix

Mapping Tailscale's admin IA to bamboo's current state:

| Tailscale | bamboo today | Gap |
|---|---|---|
| Machines | 節點 (peers) ✓ | Missing: owner column, addresses expand (DNS short names), version-upgrade indicator, kebab-menu row actions |
| Apps | — | Not built |
| Services | — | Not built (subnet routes / exit nodes) |
| Users | — | **Not built — biggest gap** |
| Access controls | ACL ✓ | Read-only; needs HCL editor + Preview/Test/Edit tabs |
| Logs | Audit feed (partial) | We have audit log; missing per-device connection log (ClickHouse-backed) |
| DNS | — | Not built (MagicDNS / tenant subdomain / nameservers) |
| Settings | — | No dedicated page; needs tenant settings + webhooks + API keys |
| Resource hub | — | No docs site |
| Pre-auth keys | ✓ top-nav page | Tailscale keeps under Settings → Keys. Consider moving when Settings ships |
| Dashboard | ✓ totals + activity | Tailscale has none — Machines IS their overview. Ours is fine |

### 3.2 Prioritization

**P0 (next sprint — fills the most visible gap):**

- **Users page + role model** (Owner / Admin / Member). Tenant-level user
  management, invite flow via email link. Adds the missing IA tab and
  unblocks multi-user testing.
- **Machine row enhancements**: owner column, addresses expand (DNS name +
  public-key dropdown), version-upgrade indicator. Closes the visible gap
  with Tailscale's machines table.
- **Settings page skeleton**: tenant ID display, display name, future home
  for Pre-auth keys (move from top nav)
- **Top-nav IA expansion** (sidebar): 4 → 6 items
  - 總覽 (dashboard)
  - 節點 (peers)
  - Users
  - 存取政策 (access)
  - 日誌 (logs — placeholder for now)
  - 設定 (settings)

  Pre-auth keys moves under Settings.

**P1 (differentiation / vendor-grade):**

- **DNS page**: tenant name display, MagicDNS toggle, global nameservers,
  DNS search domain
- **Access controls editor**: Monaco / CodeMirror with HCL highlighting +
  Preview/Test/Edit/Examples tabs. Tailscale's editor is the reference
- **Logs page**: device connection timeline (peer ↔ peer), ClickHouse-backed
- **Subnet routes / Exit nodes**: enter through Machine detail (drawer) —
  advertise routes, approve as exit node

**P2 (scale / commercial):**

- Webhooks (`settings/webhooks`) — push events to external endpoints
- API tokens UI (`settings/api-keys`) — CI / script credentials
- Apps integration (OAuth SaaS apps locked to tailnet)
- Billing / plan management
- Resource hub (docs site — `docs/demo.md` expanded into a site)

### 3.3 Out of scope (not on the Tailscale-clone path)

- Apple-style polish that doesn't ship value (animations, illustrations)
- Mobile-first redesign — desktop primary for now, mobile is drawer + page
- Custom themes / per-tenant branding

## 4. Implementation order (suggested)

1. ✅ Visual stack (PRs #69-#73) — done / under review
2. Design doc (this) — locks decisions
3. Users API + page (P0-1) — backend + frontend, ~1-2 sprints
4. Machine row enhancements (P0-2) — frontend only
5. Settings page skeleton (P0-3) — frontend; absorb Pre-auth keys
6. Sidebar nav expansion (P0-4) — wait until Settings + Users exist
7. P1 items in order: DNS → ACL editor → Logs → Subnet routes

Each item gets its own PR or stacked PR series. Visual work always lands first
where there's overlap — backend should never block visual iteration.

## 5. Open questions

- **Dark mode**: currently zinc-950 / zinc-100 (cool dark). Should we add a
  proper `bamboo-950` warm-dark stop? Or accept the cold dark since it's a
  secondary surface?
- **Mobile drawer focus trap**: currently uses ESC + backdrop click. Worth
  adding a tab-trap inside the drawer? (P1 follow-up, not blocking)
- **Avatar dropdown**: TopBar still shows `email + tenant + sign-out` inline.
  Tailscale uses an avatar-only chip → dropdown. Worth converting? (Lo-pri.)
- **Tenant switching**: future-multi-tenant UX. Probably belongs in TopBar
  avatar dropdown. Deferred until a real second tenant exists.

---

This doc lives in `docs/design/` and is the canonical reference until
superseded by a follow-up doc explicitly named. If a decision here gets
overturned, update this doc rather than scattering rationale across PRs.
