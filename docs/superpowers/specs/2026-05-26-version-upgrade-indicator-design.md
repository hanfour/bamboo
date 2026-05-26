# Version-upgrade indicator on PeerTable — design (2026-05-26)

## Why

Last item on the §6 visual polish list in
`docs/design/2026-05-17-feature-gap-roadmap.md`. Admins viewing PeerTable
should see at a glance which peers are running an outdated client, so
they can nudge those users to upgrade. Closes the only remaining ❌ in §6.

## Scope summary

- **CLI**: already wires ldflag `Version` into the `clientVersion` JSON
  field of register requests. **No change required.**
- **Apple**: currently hard-codes `clientVersion: "0.0.1"` in two
  places (`clients/apple/Shared/ConnectionViewModel.swift:461, 953`).
  Replace with `CFBundleShortVersionString` from `Bundle.main`.
- **Controller**: new `internal/releasefeed` package polls GitHub
  releases hourly; new `version_compare.go` helper does `semver.Compare`;
  list-peers handler embeds `latestClientVersion` + per-peer
  `upgradeAvailable` boolean in the response.
- **Web**: PeerTable gains a "Client ver" column between OS and Status.
  Renders the version string; appends an amber `↑ X.Y.Z` badge when the
  peer is strictly behind latest.

## Non-goals

- Auto-update / push-upgrade flows. This indicator is informational only.
- Per-tenant upgrade policies. Latest is instance-wide.
- Comparing across major versions specially (no major / minor warning
  tier — single `<` rule). May be revisited later.
- HCL syntax highlighting (separate roadmap item, declined for bundle cost).

## Architecture overview

```
GitHub releases API
        ↓ poll hourly (BAMBOO_RELEASE_FEED_INTERVAL, min 5m)
[controller] internal/releasefeed
  - in-memory cache: latest, lastFetch, lastErr, consecutiveFailures
  - method-on-nil-receiver-safe Latest() so handler stays branchless
        ↓
[controller] api_peers list handler
  - reads feed.Latest()
  - computes upgradeAvailable per peer via golang.org/x/mod/semver
        ↓
[web] PeerTable
  - new "Client ver" column (responsive: hidden below lg)
  - amber ↑ badge when peer.upgradeAvailable
  - hover tooltip "X.Y.Z → A.B.C available"

[apple] ConnectionViewModel
  - Bundle.main.infoDictionary["CFBundleShortVersionString"] ?? "0.0.0"
    (fallback non-empty so controller never sees blank ⇒ false-positive
    "no version known" path)
```

Components stay isolated: `releasefeed` knows nothing about peers;
handler knows nothing about GitHub; Web does no semver compare; Apple
change is purely the version string source.

## Component: `internal/releasefeed`

### Shape

```go
package releasefeed

type Feed struct {
    repo       string        // "hanfour/bamboo"
    httpClient *http.Client  // 5s timeout
    interval   time.Duration

    mu                  sync.RWMutex
    latest              string    // "0.1.4" — leading "v" stripped
    lastFetch           time.Time
    lastErr             error
    consecutiveFailures int
}

// New returns nil when disabled (caller's responsibility — see config).
func New(repo string, interval time.Duration) *Feed

// Run starts the background poller. Fires once immediately then on
// time.Ticker(interval). Returns when ctx is cancelled. Safe to call
// on a nil receiver (no-op).
func (f *Feed) Run(ctx context.Context)

// Latest reports the last successfully-fetched release tag (without
// "v" prefix) and whether it is currently considered valid.
// Returns ("", false) on a nil receiver. Returns ("", false) when
// consecutiveFailures has crossed the staleness threshold (see below).
func (f *Feed) Latest() (string, bool)
```

### Behaviour

- **First fetch** runs immediately in `Run`, before the ticker tick.
- **Endpoint**: `GET https://api.github.com/repos/{repo}/releases/latest`.
  This endpoint already excludes pre-releases (GitHub's contract).
- **User-Agent** header: `bamboo-controller/{version}` — GitHub requires
  a UA on all API calls. Pulls controller's own ldflag-injected version.
- **Tag normalisation**: response `tag_name` like `"v0.1.4"` is stripped
  to `"0.1.4"` for storage and downstream compare.
- **HTTP timeout**: 5 seconds per request (well under controller's
  goroutine budget).
- **Failure handling**:
  - On error, log a warning, increment `consecutiveFailures`, and
    **keep** the prior `latest` (if any). The badge stays accurate
    through a transient GitHub outage.
  - After 10 consecutive failures (~10h with default interval), clear
    `latest` and log an error. Better to hide the column than to show
    badges based on hours-old data with no way for the operator to know.
- **Disabled mode**: caller passes a nil `*Feed`. All methods on nil
  receivers are no-ops; `Latest()` returns `("", false)`.

### Tests

`internal/releasefeed/feed_test.go`:

- `TestFeed_FirstFetch_Success` — httptest server returns `tag_name=v0.1.4`;
  `Latest()` returns `("0.1.4", true)`.
- `TestFeed_FailureKeepsPriorValue` — first fetch succeeds, second 5xx;
  `Latest()` still returns the first value.
- `TestFeed_StaleThreshold` — 10 consecutive failures; `Latest()` returns
  `("", false)`.
- `TestFeed_NilReceiver` — `(*Feed)(nil).Latest()` returns `("", false)`
  and `(*Feed)(nil).Run(ctx)` returns immediately.
- `TestFeed_TagStripsLeadingV` — `tag_name=v1.2.3` stored as `"1.2.3"`.
- `TestFeed_MalformedJSON` — body is gibberish; counts as failure, keeps
  prior value.
- `TestFeed_Timeout` — server hangs > 5s; treated as failure.

## Component: controller config

`internal/config/config.go` adds:

```go
type ReleaseFeedConfig struct {
    Enabled  bool          `yaml:"enabled"`
    Repo     string        `yaml:"repo"`
    Interval time.Duration `yaml:"interval"`
}
// embedded in top-level Config
```

`applyEnvOverrides` reads:

| Env var                         | Default              | Notes                          |
|---------------------------------|----------------------|--------------------------------|
| `BAMBOO_RELEASE_FEED_ENABLED`   | `true`               | Set `false` for air-gapped     |
| `BAMBOO_RELEASE_FEED_REPO`      | `hanfour/bamboo`     | `owner/repo`, validated as such |
| `BAMBOO_RELEASE_FEED_INTERVAL`  | `1h`                 | Parsed via `time.ParseDuration` |

`validate()`:
- Interval `< 5m` is clamped to `5m` with a warning log (defends GitHub
  rate limit headroom: 60/hr unauthenticated).
- `Repo` regex `^[^/]+/[^/]+$` else warn and disable.

### Tests
`internal/config/config_test.go` extends to cover the three env vars
plus the `< 5m` clamp.

## Component: controller version-compare helper

New file `internal/server/version_compare.go`:

```go
// upgradeAvailable returns true iff `peer` is strictly behind `latest`
// using golang.org/x/mod/semver. Empty peer → false (we don't know,
// don't badge). Malformed either side → false (defensive: a v-less /
// non-semver string must not produce a false positive).
//
// semver lib expects leading "v"; both sides get normalised before
// Compare.
func upgradeAvailable(peerVersion, latest string) bool { ... }

func normalizeSemver(s string) string {
    if s == "" { return "" }
    if s[0] != 'v' { return "v" + s }
    return s
}
```

### Tests
`internal/server/version_compare_test.go`:

- `0.1.3` vs `0.1.4` → true
- `0.1.4` vs `0.1.4` → false (equal)
- `0.1.5` vs `0.1.4` → false (ahead)
- `""` peer → false
- `""` latest → false
- `"dev"` peer (malformed) → false
- `"v0.1.3"` (already prefixed) vs `"0.1.4"` → true (normaliser handles both shapes)
- pre-release: `0.1.4-rc1` vs `0.1.4` → true (rc is strictly before)

## Component: controller list-peers handler

### Response augmentation

`ListPeers` handler (controller-side; the route already shared by Web)
adds two fields:

```jsonc
{
  "peers": [
    {
      "id": "...",
      "clientVersion": "0.1.2",
      "upgradeAvailable": true,    // NEW; omitempty when latest unknown
      ...
    }
  ],
  "latestClientVersion": "0.1.4",  // NEW; "" when feed disabled or stale
  // ... existing fields ...
}
```

Handler computes `upgradeAvailable` per peer using `upgradeAvailable()`
helper, passing `feed.Latest()` once before the loop.

### Tests
`internal/server/api_peers_test.go` extends:

- `TestListPeers_LatestClientVersion_Embedded` — fixture feed returns
  `"0.1.4"`; response carries the field + per-peer flags.
- `TestListPeers_LatestClientVersion_Disabled` — nil feed; response
  omits `latestClientVersion` (or sends `""`) and every
  `upgradeAvailable` is `false`.
- `TestListPeers_UpgradeAvailable_PerPeer` — three peers at 0.1.3 /
  0.1.4 / 0.1.5 → flags true / false / false.

## Component: Apple bundle version

`clients/apple/Shared/ConnectionViewModel.swift`:

```swift
private static var bundleVersion: String {
    (Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String) ?? "0.0.0"
}
```

Both `clientVersion: "0.0.1"` sites (lines 461 + 953) become
`clientVersion: Self.bundleVersion`. `"0.0.0"` fallback (not `""`) so
controller never sees blank — blank would mean "I don't know what
version I am", which is a different signal than "I'm prehistoric".

### Tests
`clients/apple/Tests/AppleShared/BundleVersionTests.swift`:

- Bundle has `CFBundleShortVersionString` ⇒ returns that value.
- Bundle missing the key ⇒ returns `"0.0.0"`.

## Component: Web PeerTable

### Column layout

`apps/web/src/components/PeerTable.tsx`: new "Client ver" column between
OS and Status. Responsive: hidden below `lg` (1024px), matching
existing DNS / Tags column hiding so we don't push the table into a
scroll on narrower windows.

### Cell rendering

```tsx
function ClientVersionCell({
  version,
  latest,
  upgradeAvailable,
}: { version?: string; latest?: string; upgradeAvailable?: boolean }) {
  if (!version) return <span className="text-bamboo-200/40">—</span>;
  if (!upgradeAvailable) return <span>{version}</span>;
  return (
    <span title={`${version} → ${latest} available`}>
      {version}{' '}
      <span className="text-amber-300">↑ {latest}</span>
    </span>
  );
}
```

- Empty version → `—` in muted bamboo
- Up-to-date / ahead → plain version string
- Behind → version + amber arrow with hover tooltip

### i18n

`apps/web/messages/en.json` + `zh-TW.json` gain:

```json
"peerTable": {
  "columns": {
    "clientVersion": "Client ver"       // zh-TW: "用戶端版本"
  },
  "upgradeAvailable": "{current} → {latest} available"
                      // zh-TW: "可升級 {current} → {latest}"
}
```

### Type changes

`apps/web/src/lib/types.ts`:
- `Peer.clientVersion?: string`
- `Peer.upgradeAvailable?: boolean`
- list response: `latestClientVersion?: string`

### Tests (Web)

PeerTable already has component tests. Extend with:
- Cell renders `—` for empty version
- Cell renders plain version when not upgradeAvailable
- Cell renders amber ↑ when upgradeAvailable

## PR split

Four PRs in dependency order:

1. **`feat(controller): releasefeed package + semver compare helper`**
   - `internal/releasefeed/` package
   - `internal/server/version_compare.go` + test
   - Config struct + env override wiring + validation
   - **Does not yet plug into handlers.** Pure libraries + config.

2. **`feat(controller): expose latestClientVersion + upgradeAvailable in /api/v1/peers`**
   - Wire `releasefeed.Feed` into `NewHTTPServer`
   - Boot lifecycle in `cmd/controller/main.go`
   - Augment `ListPeers` response
   - Handler-layer tests
   - Depends on PR 1.

3. **`feat(apple): wire CFBundleShortVersionString into register clientVersion`**
   - Two-line change in `ConnectionViewModel.swift` plus the helper
   - `BundleVersionTests.swift`
   - Independent of 1 & 2 — can land in parallel.

4. **`feat(web): PeerTable Client ver column + upgrade indicator`**
   - PeerTable column + cell component
   - i18n strings
   - Type additions
   - Depends on PR 2 (needs API surface).

CLI is unchanged.

## Risks and unknowns

- **GitHub rate limit (60/hr unauthenticated).** At 1h interval per
  controller, well within budget. Multi-tenant deployments with many
  controllers behind one egress IP could exceed it. **Mitigation**:
  staleness handling keeps badge correct during transient throttling;
  operator can set interval longer or disable.
- **GitHub API shape changes.** Reasonably stable, but worth keeping
  the request + parse code small and easy to swap.
- **Apple `MARKETING_VERSION` operationally drifts.** If the operator
  cuts a release tag `v0.1.4` but forgets to bump `MARKETING_VERSION`
  in the Xcode project, every Apple peer reports the old version and
  appears to need upgrading. **Mitigation**: doc the release checklist
  to include this bump alongside the CFBundleVersion auto-bump that
  `build-mac-dist.sh` already does.
- **No client-version backfill.** Peers that registered before this
  change ship and never re-register stay with stale / blank
  `client_version` until they next register. Acceptable for an
  informational badge.

## Open questions (none — design closed)

All clarifying questions were answered in brainstorming:
- Latest source: GitHub release feed
- Comparison: strict `<` semver
- Surface: new "Client ver" column
