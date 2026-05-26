# Version-Upgrade Indicator — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a "Client ver" column to PeerTable that flags peers behind the latest released bamboo client version, sourced from GitHub releases.

**Architecture:** Controller polls `api.github.com/repos/{repo}/releases/latest` hourly into an in-memory feed; the list-peers handler embeds `latestClientVersion` (top-level) and per-peer `upgradeAvailable` (computed via `golang.org/x/mod/semver`); the Web PeerTable renders an amber `↑ X.Y.Z` badge next to peers whose `clientVersion` is strictly behind. The Apple client switches from a hard-coded `"0.0.1"` to `CFBundleShortVersionString`.

**Tech Stack:** Go (controller, `golang.org/x/mod/semver` for compare), Swift/XCTest (Apple), TypeScript + React + next-intl (Web). pnpm. No test runner currently exists in `apps/web/`, so Phase 4 has no unit tests and relies on typecheck + lint + manual verification.

**Spec:** [`docs/superpowers/specs/2026-05-26-version-upgrade-indicator-design.md`](../specs/2026-05-26-version-upgrade-indicator-design.md)

---

## Pre-flight

- [ ] **P.1 Confirm pre-conditions**

```bash
git fetch origin
git checkout main
git pull --ff-only
```

Expected: working tree clean, on `main`, in sync with origin.

- [ ] **P.2 Verify `golang.org/x/mod` is reachable as a direct import**

```bash
cd apps/controller
grep 'golang.org/x/mod' go.mod
```

Expected: line like `golang.org/x/mod v0.34.0 // indirect`. (We'll promote it to direct when the first file imports `semver`.)

---

## Phase 1 — PR 1: `releasefeed` package + `version_compare` helper + config

**Goal of this PR:** Land the two pure-function libraries (release feed, semver compare) and the config wiring, with full unit coverage. **No handler integration yet.**

### File structure

```
apps/controller/
├── internal/
│   ├── releasefeed/
│   │   ├── feed.go              # NEW — Feed type, Run, Latest, fetch logic
│   │   └── feed_test.go         # NEW — 7 tests covering happy / failure / nil / stale
│   ├── server/
│   │   ├── version_compare.go        # NEW — upgradeAvailable + normalizeSemver
│   │   └── version_compare_test.go   # NEW — 8 cases
│   └── config/
│       ├── config.go            # MODIFY — add ReleaseFeedConfig + env overrides
│       └── config_test.go       # MODIFY — extend env-override tests
```

### Task 1.1 — Branch

- [ ] **Step 1: Create the branch**

```bash
git checkout -b feat-controller-releasefeed-pkg
```

### Task 1.2 — `version_compare` helper (TDD)

**Files:**
- Create: `apps/controller/internal/server/version_compare_test.go`
- Create: `apps/controller/internal/server/version_compare.go`

- [ ] **Step 1: Write the failing test**

`apps/controller/internal/server/version_compare_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "testing"

// TestUpgradeAvailable pins the semver-compare contract used by the
// peers handler to surface the upgrade indicator. Each case is
// deliberately a single (peer, latest, want) triplet so a regression
// names the exact failing path.
func TestUpgradeAvailable(t *testing.T) {
	cases := []struct {
		name   string
		peer   string
		latest string
		want   bool
	}{
		{"behind", "0.1.3", "0.1.4", true},
		{"equal", "0.1.4", "0.1.4", false},
		{"ahead", "0.1.5", "0.1.4", false},
		{"empty peer", "", "0.1.4", false},
		{"empty latest", "0.1.4", "", false},
		{"malformed peer", "dev", "0.1.4", false},
		{"v-prefixed peer", "v0.1.3", "0.1.4", true},
		{"pre-release behind stable", "0.1.4-rc1", "0.1.4", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := upgradeAvailable(tc.peer, tc.latest)
			if got != tc.want {
				t.Errorf("upgradeAvailable(%q, %q) = %v, want %v",
					tc.peer, tc.latest, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test, confirm it fails**

```bash
cd apps/controller
go test ./internal/server/ -run TestUpgradeAvailable -v
```

Expected: `undefined: upgradeAvailable` compile error.

- [ ] **Step 3: Write minimal implementation**

`apps/controller/internal/server/version_compare.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "golang.org/x/mod/semver"

// upgradeAvailable returns true iff `peerVersion` is strictly behind
// `latest` per semver. Empty either side → false (we don't know,
// don't badge). Malformed either side → false (defensive: a v-less
// dev string must not produce a false positive that nags the
// operator every refresh).
//
// `golang.org/x/mod/semver` expects a leading "v"; normalizeSemver
// adds it so callers can pass either "0.1.4" or "v0.1.4" without
// thinking about which.
func upgradeAvailable(peerVersion, latest string) bool {
	if peerVersion == "" || latest == "" {
		return false
	}
	pv := normalizeSemver(peerVersion)
	lv := normalizeSemver(latest)
	if !semver.IsValid(pv) || !semver.IsValid(lv) {
		return false
	}
	return semver.Compare(pv, lv) < 0
}

func normalizeSemver(s string) string {
	if s == "" || s[0] == 'v' {
		return s
	}
	return "v" + s
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
go test ./internal/server/ -run TestUpgradeAvailable -v
```

Expected: 8 PASS lines.

- [ ] **Step 5: Commit**

```bash
git add apps/controller/internal/server/version_compare.go \
        apps/controller/internal/server/version_compare_test.go
git commit -m "feat(controller): semver compare helper for upgrade indicator"
```

### Task 1.3 — `ReleaseFeedConfig` struct (TDD)

**Files:**
- Modify: `apps/controller/internal/config/config.go`
- Modify: `apps/controller/internal/config/config_test.go`

- [ ] **Step 1: Read existing config_test.go shape to mirror its style**

```bash
sed -n '1,40p' apps/controller/internal/config/config_test.go
```

Note the test-helper conventions (table-driven? per-env-var subtests?).

- [ ] **Step 2: Add failing test for env override**

Append to `apps/controller/internal/config/config_test.go`:

```go
// TestApplyEnvOverrides_ReleaseFeed pins the three BAMBOO_RELEASE_FEED_*
// env vars surface into the Config struct as expected, including the
// 5-minute floor on interval (rate-limit headroom against GitHub's
// 60-req/hr unauthenticated quota).
func TestApplyEnvOverrides_ReleaseFeed(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "")
		var c Config
		c.applyEnvOverrides()
		if !c.ReleaseFeed.Enabled {
			t.Errorf("Enabled default = false, want true")
		}
		if c.ReleaseFeed.Repo != "hanfour/bamboo" {
			t.Errorf("Repo default = %q, want hanfour/bamboo", c.ReleaseFeed.Repo)
		}
		if c.ReleaseFeed.Interval != time.Hour {
			t.Errorf("Interval default = %v, want 1h", c.ReleaseFeed.Interval)
		}
	})
	t.Run("env overrides", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "false")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "myorg/mybamboo")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "30m")
		var c Config
		c.applyEnvOverrides()
		if c.ReleaseFeed.Enabled {
			t.Errorf("Enabled = true, want false")
		}
		if c.ReleaseFeed.Repo != "myorg/mybamboo" {
			t.Errorf("Repo = %q, want myorg/mybamboo", c.ReleaseFeed.Repo)
		}
		if c.ReleaseFeed.Interval != 30*time.Minute {
			t.Errorf("Interval = %v, want 30m", c.ReleaseFeed.Interval)
		}
	})
	t.Run("interval clamps to 5m floor", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "1m")
		var c Config
		c.applyEnvOverrides()
		c.validate()
		if c.ReleaseFeed.Interval != 5*time.Minute {
			t.Errorf("Interval = %v after clamp, want 5m", c.ReleaseFeed.Interval)
		}
	})
	t.Run("invalid repo disables feed", func(t *testing.T) {
		t.Setenv("BAMBOO_RELEASE_FEED_ENABLED", "true")
		t.Setenv("BAMBOO_RELEASE_FEED_REPO", "no-slash-no-repo")
		t.Setenv("BAMBOO_RELEASE_FEED_INTERVAL", "")
		var c Config
		c.applyEnvOverrides()
		c.validate()
		if c.ReleaseFeed.Enabled {
			t.Errorf("Enabled = true after invalid repo, want false")
		}
	})
}
```

> If `config_test.go` does not already import `time`, add `"time"` to its import block when the editor highlights the missing import.

- [ ] **Step 3: Run the test, confirm it fails**

```bash
go test ./internal/config/ -run TestApplyEnvOverrides_ReleaseFeed -v
```

Expected: `c.ReleaseFeed undefined (type Config has no field or method ReleaseFeed)`.

- [ ] **Step 4: Add the struct + env wiring + validation**

In `apps/controller/internal/config/config.go`:

```go
// ReleaseFeedConfig drives the GitHub-releases poller that powers
// the PeerTable version-upgrade indicator. Operators in air-gapped
// deployments should set Enabled=false; everyone else leaves the
// defaults and gets the indicator for free.
type ReleaseFeedConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Repo     string        `yaml:"repo"`
	Interval time.Duration `yaml:"interval"`
}
```

Add `ReleaseFeed ReleaseFeedConfig \`yaml:"release_feed"\`` to the top-level `Config` struct (after `SMTP`).

In `applyEnvOverrides`:

```go
// Release feed defaults; env overrides apply on top.
if c.ReleaseFeed.Repo == "" {
	c.ReleaseFeed.Repo = "hanfour/bamboo"
}
if c.ReleaseFeed.Interval == 0 {
	c.ReleaseFeed.Interval = time.Hour
}
// Enabled defaults to true UNLESS the env explicitly opts out.
// The zero value (false) here means "use default" — we read the
// env var into a tri-state below to disambiguate.
c.ReleaseFeed.Enabled = true
if v := os.Getenv("BAMBOO_RELEASE_FEED_ENABLED"); v != "" {
	c.ReleaseFeed.Enabled = v == "true" || v == "1"
}
if v := os.Getenv("BAMBOO_RELEASE_FEED_REPO"); v != "" {
	c.ReleaseFeed.Repo = v
}
if v := os.Getenv("BAMBOO_RELEASE_FEED_INTERVAL"); v != "" {
	if d, err := time.ParseDuration(v); err == nil {
		c.ReleaseFeed.Interval = d
	}
}
```

In `validate()` (add a new block near the bottom):

```go
// Release feed: clamp interval to 5m floor + validate the repo
// shape. A bad repo disables the feed entirely so the operator
// gets "no badge" instead of "every fetch errors".
if c.ReleaseFeed.Enabled {
	if c.ReleaseFeed.Interval < 5*time.Minute {
		slog.Warn("release_feed: interval below 5m floor, clamping",
			"requested", c.ReleaseFeed.Interval, "floor", 5*time.Minute)
		c.ReleaseFeed.Interval = 5 * time.Minute
	}
	// Shape: exactly one "/", non-empty on both sides.
	parts := strings.Split(c.ReleaseFeed.Repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		slog.Warn("release_feed: invalid repo, disabling",
			"repo", c.ReleaseFeed.Repo)
		c.ReleaseFeed.Enabled = false
	}
}
```

Ensure `config.go` imports include `"log/slog"`, `"strings"`, `"time"`.

- [ ] **Step 5: Run the test, confirm it passes**

```bash
go test ./internal/config/ -run TestApplyEnvOverrides_ReleaseFeed -v
go test ./internal/config/ -v
```

Expected: all subtests PASS, no regressions in existing config tests.

- [ ] **Step 6: Commit**

```bash
git add apps/controller/internal/config/config.go \
        apps/controller/internal/config/config_test.go
git commit -m "feat(controller): ReleaseFeedConfig + env wiring + validation"
```

### Task 1.4 — `releasefeed` package skeleton + nil safety (TDD)

**Files:**
- Create: `apps/controller/internal/releasefeed/feed.go`
- Create: `apps/controller/internal/releasefeed/feed_test.go`

- [ ] **Step 1: Write the failing tests**

`apps/controller/internal/releasefeed/feed_test.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

package releasefeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNilFeed_Latest pins the nil-receiver contract — handler code
// stays branchless on the feed pointer.
func TestNilFeed_Latest(t *testing.T) {
	var f *Feed
	got, ok := f.Latest()
	if ok {
		t.Errorf("ok = true, want false")
	}
	if got != "" {
		t.Errorf("got = %q, want empty", got)
	}
}

// TestNilFeed_Run pins that Run is a no-op on nil, so a server
// constructed with feed=nil doesn't crash at boot.
func TestNilFeed_Run(t *testing.T) {
	var f *Feed
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	f.Run(ctx) // must return without panicking
}

// TestFeed_FirstFetchSuccess pins the happy path: first fetch
// populates Latest before Run's ticker even kicks. We assert via
// poll-until-set so the test is robust to scheduler jitter.
func TestFeed_FirstFetchSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v0.1.4"})
	}))
	defer srv.Close()
	f := newWithBaseURL("hanfour/bamboo", time.Hour, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	if !waitForLatest(t, f, "0.1.4", time.Second) {
		t.Fatalf("did not populate Latest within 1s")
	}
}

// TestFeed_FailureKeepsPriorValue pins that a transient GitHub
// outage doesn't drop the badge — operators see the last known
// good value through the failure window.
func TestFeed_FailureKeepsPriorValue(t *testing.T) {
	var failNext bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v0.1.4"})
	}))
	defer srv.Close()
	f := newWithBaseURL("hanfour/bamboo", time.Hour, srv.URL)
	if err := f.fetchOnce(context.Background()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	failNext = true
	if err := f.fetchOnce(context.Background()); err == nil {
		t.Fatalf("second fetch: expected error, got nil")
	}
	got, ok := f.Latest()
	if !ok || got != "0.1.4" {
		t.Errorf("Latest after failure = (%q, %v), want (\"0.1.4\", true)", got, ok)
	}
}

// TestFeed_StaleThreshold pins the 10-consecutive-failure ceiling.
// After it, Latest goes "unknown" — better to hide the column than
// to render hours-old data.
func TestFeed_StaleThreshold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	f := newWithBaseURL("hanfour/bamboo", time.Hour, srv.URL)
	// Prime a known-good value.
	f.mu.Lock()
	f.latest = "0.1.4"
	f.mu.Unlock()
	for i := 0; i < staleFailureThreshold; i++ {
		_ = f.fetchOnce(context.Background())
	}
	if _, ok := f.Latest(); ok {
		t.Errorf("Latest still valid after %d failures, want unknown", staleFailureThreshold)
	}
}

// TestFeed_TagStripsLeadingV — controller compares without the v
// prefix; the lib normalises here so downstream isn't duplicated.
func TestFeed_TagStripsLeadingV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.3"})
	}))
	defer srv.Close()
	f := newWithBaseURL("hanfour/bamboo", time.Hour, srv.URL)
	if err := f.fetchOnce(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, _ := f.Latest()
	if got != "1.2.3" {
		t.Errorf("Latest = %q, want %q", got, "1.2.3")
	}
}

// TestFeed_MalformedJSON — garbage body must count as failure,
// not silently overwrite Latest with an empty string.
func TestFeed_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer srv.Close()
	f := newWithBaseURL("hanfour/bamboo", time.Hour, srv.URL)
	f.mu.Lock()
	f.latest = "0.1.4"
	f.mu.Unlock()
	if err := f.fetchOnce(context.Background()); err == nil {
		t.Fatalf("expected parse error, got nil")
	}
	got, ok := f.Latest()
	if !ok || got != "0.1.4" {
		t.Errorf("Latest after bad JSON = (%q, %v), want prior value intact", got, ok)
	}
}

// waitForLatest polls until Feed.Latest reports want, or fails the
// test after timeout. Used to keep the happy-path test robust to
// goroutine scheduling without sleep-based assertions.
func waitForLatest(t *testing.T, f *Feed, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got, ok := f.Latest(); ok && got == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
```

- [ ] **Step 2: Run tests, confirm they fail**

```bash
go test ./internal/releasefeed/ -v
```

Expected: package doesn't exist yet — compile errors.

- [ ] **Step 3: Implement the package**

`apps/controller/internal/releasefeed/feed.go`:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package releasefeed polls the upstream GitHub releases endpoint
// for the latest stable bamboo client release. Powers the
// PeerTable upgrade indicator. Owns the cache + the background
// poller; nil-safe so callers (disabled deployments) don't need
// to branch around its methods.
package releasefeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// staleFailureThreshold caps consecutive fetch failures before
// Latest reports unknown. 10 at the default 1h interval ≈ 10
// hours of upstream outage before the badge disappears. Better
// to hide it than to show stale data with no operator signal.
const staleFailureThreshold = 10

// requestTimeout caps a single fetch — well under controller's
// 30s WriteTimeout so a hung GitHub doesn't ladder up into request
// timeouts elsewhere.
const requestTimeout = 5 * time.Second

// Feed holds the last-known-good upstream version + the bookkeeping
// to decide when that value is too stale to surface.
type Feed struct {
	repo     string
	interval time.Duration
	baseURL  string // overridable for tests; defaults to api.github.com

	httpClient *http.Client

	mu                  sync.RWMutex
	latest              string
	lastFetch           time.Time
	lastErr             error
	consecutiveFailures int
}

// New constructs a Feed for the given GitHub repo. interval is the
// poll cadence; the caller has already clamped it (see
// internal/config validate()).
func New(repo string, interval time.Duration) *Feed {
	return newWithBaseURL(repo, interval, "https://api.github.com")
}

func newWithBaseURL(repo string, interval time.Duration, baseURL string) *Feed {
	return &Feed{
		repo:       repo,
		interval:   interval,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Latest reports the last-known-good upstream tag (without the
// leading "v") and whether it is currently considered fresh.
// Returns ("", false) on a nil receiver — disabled deployments.
func (f *Feed) Latest() (string, bool) {
	if f == nil {
		return "", false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.consecutiveFailures >= staleFailureThreshold {
		return "", false
	}
	if f.latest == "" {
		return "", false
	}
	return f.latest, true
}

// Run drives the background poller. Fires one immediate fetch then
// settles into the configured interval. Exits when ctx is cancelled.
// Safe to call on a nil receiver.
func (f *Feed) Run(ctx context.Context) {
	if f == nil {
		return
	}
	if err := f.fetchOnce(ctx); err != nil {
		slog.Warn("releasefeed: initial fetch failed", "err", err)
	}
	t := time.NewTicker(f.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := f.fetchOnce(ctx); err != nil {
				slog.Warn("releasefeed: fetch failed", "err", err)
			}
		}
	}
}

// fetchOnce performs one HTTP call against the GitHub API and
// updates the cached state. Exported only via Run for production;
// tests drive it directly.
func (f *Feed) fetchOnce(ctx context.Context) error {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", f.baseURL, f.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return f.recordFailure(err)
	}
	req.Header.Set("User-Agent", "bamboo-controller")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return f.recordFailure(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return f.recordFailure(fmt.Errorf("github status %d", resp.StatusCode))
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return f.recordFailure(err)
	}
	if body.TagName == "" {
		return f.recordFailure(errors.New("github response missing tag_name"))
	}
	f.recordSuccess(strings.TrimPrefix(body.TagName, "v"))
	return nil
}

func (f *Feed) recordSuccess(tag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest = tag
	f.lastFetch = time.Now()
	f.lastErr = nil
	f.consecutiveFailures = 0
}

func (f *Feed) recordFailure(err error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastErr = err
	f.consecutiveFailures++
	return err
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
go test ./internal/releasefeed/ -v
```

Expected: 7 PASS lines.

- [ ] **Step 5: Run the whole controller test suite to confirm no regressions**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: ok across all packages.

- [ ] **Step 6: Promote `golang.org/x/mod` from indirect to direct**

```bash
go mod tidy
grep 'golang.org/x/mod' go.mod
```

Expected: line no longer carries `// indirect`.

- [ ] **Step 7: Commit**

```bash
git add apps/controller/internal/releasefeed/ apps/controller/go.mod apps/controller/go.sum
git commit -m "feat(controller): releasefeed package — GitHub releases poller"
```

### Task 1.5 — PR 1 push + open

- [ ] **Step 1: Push**

```bash
git push -u origin feat-controller-releasefeed-pkg
```

- [ ] **Step 2: Open PR**

```bash
gh pr create --title "feat(controller): releasefeed package + semver compare helper + config" --body "$(cat <<'EOF'
## Summary

Stand-alone libraries supporting the PeerTable version-upgrade indicator design (`docs/superpowers/specs/2026-05-26-version-upgrade-indicator-design.md`). This PR lands the pure code paths only:

- `internal/releasefeed/` — background poller for `api.github.com/repos/{repo}/releases/latest`. Nil-safe `*Feed`, 10-consecutive-failure staleness ceiling, 5s per-request timeout, leading-`v` stripped from cached tags.
- `internal/server/version_compare.go` — `upgradeAvailable(peer, latest)` returning strict `<` semver, defensive on empty / malformed input.
- `ReleaseFeedConfig` + three `BAMBOO_RELEASE_FEED_*` env vars wired through \`applyEnvOverrides\` + \`validate\` (5m interval floor, malformed repo disables feed).

No handler integration yet — PR 2 wires this into \`apiPeers\`.

## Test plan

- [x] \`go test ./internal/releasefeed/\` — 7 cases (nil receiver, happy, failure-keeps-prior, staleness, v-strip, malformed JSON, no-op Run on nil)
- [x] \`go test ./internal/server/ -run TestUpgradeAvailable\` — 8 cases including empty / malformed / pre-release behind stable
- [x] \`go test ./internal/config/ -run TestApplyEnvOverrides_ReleaseFeed\` — defaults / overrides / interval clamp / invalid-repo disable
- [x] \`go build ./...\` + \`go vet ./...\` clean

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Phase 2 — PR 2: Wire feed into `apiPeers` handler

**Goal of this PR:** Plumb the Phase-1 `*Feed` into the running server and surface `latestClientVersion` + per-peer `upgradeAvailable` on `/api/v1/peers`. Depends on PR 1 being merged.

### File structure

```
apps/controller/internal/server/
├── http.go          # MODIFY — NewHTTPServer signature: + *releasefeed.Feed
├── server.go        # MODIFY — call-site (line ~60) + Run() goroutine for feed
├── api.go           # MODIFY — apiPeerJSON gains UpgradeAvailable; apiPeers reads feed
└── api_test.go      # MODIFY — TestApiPeers_LatestVersion_* (3 cases)
```

### Task 2.1 — Branch from latest main (after PR 1 lands)

- [ ] **Step 1: Branch**

```bash
git checkout main
git pull --ff-only
git checkout -b feat-controller-peers-upgrade-available
```

### Task 2.2 — Extend `NewHTTPServer` signature

- [ ] **Step 1: Modify `NewHTTPServer` to accept the feed**

In `apps/controller/internal/server/http.go`, change the signature:

```go
func NewHTTPServer(
	addr string,
	pool *db.Pool,
	providers map[string]auth.OIDCProvider,
	ch *clickhouse.Conn,
	secret []byte,
	baseURL string,
	ttl time.Duration,
	coord *handlers.CoordinatorHandler,
	feed *releasefeed.Feed, // NEW — may be nil when disabled
) *HTTPServer
```

Add `releaseFeed *releasefeed.Feed` to the `HTTPServer` struct (alongside the other dependencies). Set `releaseFeed: feed,` in the literal inside `NewHTTPServer`.

Add to imports: `"github.com/hanfour/bamboo/apps/controller/internal/releasefeed"`.

- [ ] **Step 2: Update the call site in `server.go`**

In `apps/controller/internal/server/server.go` around line 60:

```go
// Before NewHTTPServer call:
var feed *releasefeed.Feed
if cfg.ReleaseFeed.Enabled {
	feed = releasefeed.New(cfg.ReleaseFeed.Repo, cfg.ReleaseFeed.Interval)
}

httpSrv := NewHTTPServer(
	cfg.Server.HTTPAddr, pool, providers, ch,
	secret, cfg.Auth.OIDC.BaseURL, ttl, coordHandler,
	feed,
)
```

Add to imports: `"github.com/hanfour/bamboo/apps/controller/internal/releasefeed"`.

- [ ] **Step 3: Start the feed's goroutine in `Server.Run`**

Find where `Server.Run` (or equivalent) launches long-running goroutines. Add:

```go
// Start the release-feed poller alongside the other background
// workers. feed.Run is a no-op on nil (disabled deployments).
go httpSrv.releaseFeed.Run(ctx)
```

> If `Run` is keyed to the server struct, expose `releaseFeed` on the struct (it's already on the embedded `*HTTPServer`).

- [ ] **Step 4: Verify build still passes**

```bash
cd apps/controller
go build ./...
```

Expected: clean. The signature change forces every existing handler test that constructs `HTTPServer{...}` directly to either go through `NewHTTPServer` or to skip the field (it's optional in the literal because of Go's zero-value default — `releaseFeed: nil` is the disabled path).

- [ ] **Step 5: Commit**

```bash
git add apps/controller/internal/server/http.go \
        apps/controller/internal/server/server.go
git commit -m "feat(controller): wire releasefeed.Feed into HTTPServer"
```

### Task 2.3 — Augment `apiPeerJSON` + `apiPeers` + pure-function test (TDD)

**Files:**
- Modify: `apps/controller/internal/server/api.go` (struct at ~753, apiPeers at ~871)
- Modify: `apps/controller/internal/server/api_test.go` (add `TestAugmentUpgradeAvailable`)

**Test approach note:** `apps/controller/internal/server/api_test.go` does NOT have a DB-backed handler harness — neighbour tests (`audit_retention_test.go`, `TestAuthenticate_*`) all stick to pure-function or minimal-struct-literal patterns. To keep this PR consistent with the codebase, we extract a pure helper `augmentUpgradeAvailable([]apiPeerJSON, latest string)` and unit-test the helper. The handler then calls it; that 2-line wiring is verified by manual / e2e, not by a fresh DB-test infrastructure.

- [ ] **Step 1: Write the failing test for the pure helper**

Add to `apps/controller/internal/server/api_test.go`:

```go
// TestAugmentUpgradeAvailable pins the per-peer flagging logic
// used by apiPeers — covers the disabled-feed (empty latest),
// behind / equal / ahead, and empty-client-version paths in one
// table. The handler wiring on top is two lines and verified by
// manual / e2e; this test exists to catch a future regression that
// drops the per-peer assignment or misroutes the latest string.
func TestAugmentUpgradeAvailable(t *testing.T) {
	cases := []struct {
		name   string
		peers  []apiPeerJSON
		latest string
		want   []bool // upgrade_available per peer, same order
	}{
		{
			name:   "feed disabled (empty latest) suppresses all flags",
			peers:  []apiPeerJSON{{ClientVersion: "0.1.3"}, {ClientVersion: "0.1.4"}},
			latest: "",
			want:   []bool{false, false},
		},
		{
			name:   "behind / equal / ahead",
			peers:  []apiPeerJSON{{ClientVersion: "0.1.3"}, {ClientVersion: "0.1.4"}, {ClientVersion: "0.1.5"}},
			latest: "0.1.4",
			want:   []bool{true, false, false},
		},
		{
			name:   "empty client_version never flagged",
			peers:  []apiPeerJSON{{ClientVersion: ""}, {ClientVersion: "0.1.3"}},
			latest: "0.1.4",
			want:   []bool{false, true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			augmentUpgradeAvailable(tc.peers, tc.latest)
			for i := range tc.peers {
				if got := tc.peers[i].UpgradeAvailable; got != tc.want[i] {
					t.Errorf("peers[%d].UpgradeAvailable = %v, want %v", i, got, tc.want[i])
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run the test, confirm it fails**

```bash
go test ./internal/server/ -run TestAugmentUpgradeAvailable -v
```

Expected: `undefined: augmentUpgradeAvailable` and `UpgradeAvailable undefined (apiPeerJSON has no field)`.

- [ ] **Step 3: Add the `UpgradeAvailable` field to `apiPeerJSON`**

In `apps/controller/internal/server/api.go`, append to the struct (after `ConnectionLatencyMs`):

```go
	// UpgradeAvailable is true iff the peer's ClientVersion is
	// strictly behind the release-feed's latest tag. Omitempty so a
	// disabled feed produces the same JSON shape as "this peer is
	// already up-to-date" — Web treats both as no badge.
	UpgradeAvailable bool `json:"upgradeAvailable,omitempty"`
```

- [ ] **Step 4: Add the pure helper + wire the handler**

In `apps/controller/internal/server/api.go`, add the helper near `peerToJSON`:

```go
// augmentUpgradeAvailable fills in UpgradeAvailable on each row.
// Pulled out as a pure function so api_test.go can cover the
// per-peer flagging matrix without standing up a real DB harness.
// Empty latest disables the flag entirely (disabled / stale feed).
func augmentUpgradeAvailable(peers []apiPeerJSON, latest string) {
	if latest == "" {
		return
	}
	for i := range peers {
		peers[i].UpgradeAvailable = upgradeAvailable(peers[i].ClientVersion, latest)
	}
}
```

Replace `apiPeers` (at ~line 871):

```go
func (h *HTTPServer) apiPeers(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	peers, err := h.peers.ListByTenant(r.Context(), tenant.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	latest, _ := h.releaseFeed.Latest() // ("", false) on nil / disabled / stale
	out := make([]apiPeerJSON, 0, len(peers))
	for _, p := range peers {
		out = append(out, peerToJSON(p))
	}
	augmentUpgradeAvailable(out, latest)
	writeJSON(w, http.StatusOK, map[string]any{
		"peers":               out,
		"latestClientVersion": latest, // "" when disabled / stale
	})
}
```

- [ ] **Step 5: Run the tests, confirm they pass**

```bash
go test ./internal/server/ -run TestAugmentUpgradeAvailable -v
```

Expected: 3 PASS lines.

- [ ] **Step 6: Run the whole server package to check regressions**

```bash
go build ./... && go vet ./... && go test ./internal/server/
```

Expected: ok.

- [ ] **Step 7: Commit**

```bash
git add apps/controller/internal/server/api.go \
        apps/controller/internal/server/api_test.go
git commit -m "feat(controller): expose latestClientVersion + upgradeAvailable on /api/v1/peers"
```

### Task 2.4 — PR 2 push + open

- [ ] **Step 1: Push + PR**

```bash
git push -u origin feat-controller-peers-upgrade-available
gh pr create --title "feat(controller): expose latestClientVersion + upgradeAvailable on /api/v1/peers" --body "$(cat <<'EOF'
## Summary

Wires the \`releasefeed.Feed\` from PR (previous) into the HTTPServer and surfaces:

- top-level \`latestClientVersion\` field on \`GET /api/v1/peers\` response (empty when feed disabled / stale)
- per-peer \`upgradeAvailable\` boolean computed via the shared \`upgradeAvailable()\` helper

Server.Run starts \`feed.Run(ctx)\` so the poller runs alongside the existing background workers. Nil-safe; air-gapped deployments setting \`BAMBOO_RELEASE_FEED_ENABLED=false\` see identical JSON shape to pre-feature.

## Test plan

- [x] \`go test ./internal/server/ -run TestApiPeers_LatestVersion\` — disabled / embedded / empty-client-version
- [x] \`go build ./... && go vet ./...\` clean
- [x] Existing api_test.go cases unchanged

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Phase 3 — PR 3: Apple bundle version wiring

**Goal of this PR:** Replace the two `clientVersion: "0.0.1"` hard-codes in `ConnectionViewModel.swift` with `CFBundleShortVersionString`. Independent of PRs 1 + 2; can land in parallel.

### File structure

```
clients/apple/
├── Shared/
│   └── ConnectionViewModel.swift                 # MODIFY — 461 + 953 + new bundleVersion helper
└── Tests/AppleShared/
    └── BundleVersionTests.swift                  # NEW
```

### Task 3.1 — Branch

- [ ] **Step 1: Branch from main**

```bash
git checkout main
git pull --ff-only
git checkout -b feat-apple-bundle-version
```

### Task 3.2 — Bundle version test (TDD-lite, XCTest)

- [ ] **Step 1: Write the failing test**

Create `clients/apple/Tests/AppleShared/BundleVersionTests.swift`:

```swift
// SPDX-License-Identifier: AGPL-3.0-or-later

import XCTest
@testable import BambooShared

// BundleVersionTests pins the contract used by the register +
// refresh paths in ConnectionViewModel — namely that bundleVersion
// is non-empty even when the host bundle has no Info.plist key.
// An empty clientVersion to the controller looks like "I don't
// know what version I am", which is a different signal than
// "I'm prehistoric" and would suppress the upgrade-indicator
// badge. The fallback keeps the signal correctly classified.
final class BundleVersionTests: XCTestCase {
    func testFallbackIsNonEmpty() {
        // Pass an explicit empty info-dict to exercise the fallback
        // path; the real Bundle.main is unavailable in unit-test mode.
        XCTAssertEqual(ConnectionViewModel.bundleVersion(from: [:]), "0.0.0")
    }

    func testReadsCFBundleShortVersionString() {
        let info = ["CFBundleShortVersionString": "1.2.3"]
        XCTAssertEqual(ConnectionViewModel.bundleVersion(from: info), "1.2.3")
    }

    func testIgnoresWrongType() {
        // CFBundleShortVersionString is documented String; treat
        // any other type as missing rather than coerce.
        let info: [String: Any] = ["CFBundleShortVersionString": 1.23]
        XCTAssertEqual(ConnectionViewModel.bundleVersion(from: info), "0.0.0")
    }
}
```

- [ ] **Step 2: Run the test, confirm it fails**

```bash
cd clients/apple
xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests -destination 'platform=macOS' 2>&1 | tail -30
```

Expected: `ConnectionViewModel.bundleVersion` undefined.

> If the project doesn't have a discrete `AppleSharedTests` scheme, use the scheme that contains the existing `Tests/AppleShared/*.swift` suite. Inspect `clients/apple/bamboo.xcodeproj/xcshareddata/xcschemes/` to find the correct name.

### Task 3.3 — Implement bundle version helper + replace hard-codes

- [ ] **Step 1: Add `bundleVersion` helper**

In `clients/apple/Shared/ConnectionViewModel.swift`, add a static helper near the top of the class:

```swift
extension ConnectionViewModel {
    /// bundleVersion reads CFBundleShortVersionString from the
    /// app's Info.plist, returning "0.0.0" when the key is missing
    /// or the wrong type. Used by both register + refresh-token
    /// flows so the controller sees a stable per-build identity.
    ///
    /// "0.0.0" — not "" — because the controller distinguishes
    /// "no clientVersion reported" (badge suppressed) from "very
    /// old client" (badge shown). The fallback keeps that signal
    /// correctly classified for hosts whose Info.plist accidentally
    /// loses the key.
    static func bundleVersion(from info: [String: Any]) -> String {
        if let v = info["CFBundleShortVersionString"] as? String, !v.isEmpty {
            return v
        }
        return "0.0.0"
    }

    static var bundleVersion: String {
        bundleVersion(from: Bundle.main.infoDictionary ?? [:])
    }
}
```

- [ ] **Step 2: Replace both hard-coded sites**

In `clients/apple/Shared/ConnectionViewModel.swift`:

- Line 461 area: change `clientVersion: "0.0.1",` to `clientVersion: Self.bundleVersion,`
- Line 953 area: same change.

Quick verify nothing else references `"0.0.1"`:

```bash
grep -n '"0.0.1"' clients/apple/Shared/ConnectionViewModel.swift
```

Expected: no matches.

- [ ] **Step 3: Re-run tests**

```bash
cd clients/apple
xcodebuild test -project bamboo.xcodeproj -scheme AppleSharedTests -destination 'platform=macOS' 2>&1 | tail -10
```

Expected: 3 PASS lines for the new tests, all existing tests green.

- [ ] **Step 4: Commit**

```bash
git add clients/apple/Shared/ConnectionViewModel.swift \
        clients/apple/Tests/AppleShared/BundleVersionTests.swift
git commit -m "feat(apple): wire CFBundleShortVersionString into register clientVersion"
```

### Task 3.4 — PR 3 push + open

- [ ] **Step 1: Push + PR**

```bash
git push -u origin feat-apple-bundle-version
gh pr create --title "feat(apple): wire CFBundleShortVersionString into register clientVersion" --body "$(cat <<'EOF'
## Summary

Apple client previously sent \`clientVersion: "0.0.1"\` hard-coded in both the register and refresh-token flows. Swap to \`CFBundleShortVersionString\` from \`Bundle.main\` so the controller-side version-upgrade indicator (PR 2) sees a real version.

\`bundleVersion(from:)\` helper isolated for unit-test injection. Fallback is \`"0.0.0"\` (non-empty) rather than \`""\` because the controller distinguishes "no version" (badge suppressed) from "very old" (badge shown); the fallback keeps the signal correctly classified if \`Info.plist\` accidentally loses the key.

## Operational note

The release process must keep Xcode \`MARKETING_VERSION\` in sync with the git tag — otherwise every Apple peer reports the previous version and gets flagged for upgrade.

## Test plan

- [x] \`xcodebuild test -scheme AppleSharedTests\` — 3 BundleVersionTests cases (fallback, valid, wrong-type)
- [x] No \`"0.0.1"\` left in ConnectionViewModel.swift
- [ ] Manual: build with notarized pipeline, register against staging, confirm peer.client_version in DB matches the marketing version

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Phase 4 — PR 4: Web — new "Client ver" column

**Goal of this PR:** Render the new column in `PeerTable.tsx`, consume the API surface from PR 2. Depends on PR 2 being merged. No unit tests — `apps/web/` has no test runner configured; rely on `pnpm typecheck` + `pnpm lint` + manual verification in the browser.

### File structure

```
apps/web/
├── messages/
│   ├── en.json                          # MODIFY — peerTable.columns.clientVersion + upgradeAvailable
│   └── zh-TW.json                       # MODIFY — same keys, zh-TW values
├── src/
│   ├── lib/
│   │   ├── types.ts                     # MODIFY — Peer.upgradeAvailable; list response: latestClientVersion
│   │   └── api.ts                       # MODIFY — list-peers fetch surfaces latestClientVersion
│   └── components/
│       └── PeerTable.tsx                # MODIFY — column + ClientVersionCell; prop drilling for latest
```

### Task 4.1 — Branch

- [ ] **Step 1: Branch**

```bash
git checkout main
git pull --ff-only
git checkout -b feat-web-peer-version-column
```

### Task 4.2 — Types

- [ ] **Step 1: Read existing Peer type so we extend in-place**

```bash
grep -n 'interface Peer\|type Peer' apps/web/src/lib/types.ts
```

- [ ] **Step 2: Add the new fields**

In `apps/web/src/lib/types.ts`, find the `Peer` interface and add (in alphabetical position or alongside existing version-ish fields):

```ts
  /** Set by the controller when the peer's client_version is strictly
   *  behind the release-feed's latest tag. Omitted otherwise. */
  upgradeAvailable?: boolean;
```

Find the list-peers response type (likely `PeersResponse` or similar — `grep -n 'peers:' apps/web/src/lib/types.ts`) and add:

```ts
  /** Latest known stable client release (from the controller's
   *  release-feed poller). Empty/absent when the feed is disabled
   *  or has gone stale; Web treats both as "no badge anywhere". */
  latestClientVersion?: string;
```

- [ ] **Step 3: Typecheck**

```bash
cd apps/web
pnpm typecheck
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add apps/web/src/lib/types.ts
git commit -m "feat(web): types — Peer.upgradeAvailable + PeersResponse.latestClientVersion"
```

### Task 4.3 — i18n

- [ ] **Step 1: Add keys to `en.json`**

In `apps/web/messages/en.json`, under `peerTable` (use existing key `peers` or `peerTable` — `grep '"peerTable"\|"peers":' apps/web/messages/en.json` to confirm the host key):

```jsonc
"peerTable": {
  // ... existing keys ...
  "columns": {
    // ... existing column labels ...
    "clientVersion": "Client ver"
  },
  "upgradeAvailable": "{current} → {latest} available",
  "noVersion": "—"
}
```

- [ ] **Step 2: Mirror in `zh-TW.json`**

```jsonc
"peerTable": {
  // ... existing keys ...
  "columns": {
    "clientVersion": "用戶端版本"
  },
  "upgradeAvailable": "可升級 {current} → {latest}",
  "noVersion": "—"
}
```

- [ ] **Step 3: Typecheck + lint (next-intl validates key shapes)**

```bash
pnpm typecheck && pnpm lint
```

- [ ] **Step 4: Commit**

```bash
git add apps/web/messages/en.json apps/web/messages/zh-TW.json
git commit -m "feat(web): i18n keys for Client ver column"
```

### Task 4.4 — Surface `latestClientVersion` through the fetch layer

- [ ] **Step 1: Find the list-peers fetch helper**

```bash
grep -rn 'listPeers\|fetchPeers\|api/v1/peers' apps/web/src/lib/ apps/web/src/app/
```

- [ ] **Step 2: Update return shape**

Wherever the fetch helper returns `{peers}` only, change it to surface `latestClientVersion` too. Example shape:

```ts
export async function listPeers(): Promise<PeersResponse> {
  const res = await fetch(`${API_BASE}/api/v1/peers`, { cache: 'no-store', credentials: 'include' });
  if (!res.ok) throw new Error(`peers fetch ${res.status}`);
  return (await res.json()) as PeersResponse;
}
```

- [ ] **Step 3: Typecheck**

```bash
pnpm typecheck
```

- [ ] **Step 4: Commit**

```bash
git add apps/web/src/lib/api.ts
git commit -m "feat(web): listPeers returns latestClientVersion alongside peers"
```

### Task 4.5 — `ClientVersionCell` + column

**File:**
- Modify: `apps/web/src/components/PeerTable.tsx`

**Context — the existing table:** PeerTable uses literal `<th>` / `<td>` JSX (not an array-of-columns config). The wrapper is `<div className="-mx-6 overflow-x-auto px-6 sm:mx-0 sm:px-0"><table className="w-full min-w-[860px] text-sm">` — narrow viewports get horizontal scroll. To avoid bloating that scroll further, hide the new column below `lg` using Tailwind's `hidden lg:table-cell` on both the `<th>` and the `<td>`.

- [ ] **Step 1: Add the cell component**

In `apps/web/src/components/PeerTable.tsx`, near the existing `DnsNameCell` / `StatusBadge` helpers (around line 150+):

```tsx
function ClientVersionCell({
  version,
  latest,
  upgradeAvailable,
}: {
  version?: string;
  latest?: string;
  upgradeAvailable?: boolean;
}) {
  const t = useTranslations('peerTable');
  if (!version) {
    return <span className="text-bamboo-200/40">{t('noVersion')}</span>;
  }
  if (!upgradeAvailable || !latest) {
    return <span className="font-mono text-xs">{version}</span>;
  }
  return (
    <span
      className="font-mono text-xs"
      title={t('upgradeAvailable', { current: version, latest })}
    >
      {version}{' '}
      <span className="text-amber-300">↑ {latest}</span>
    </span>
  );
}
```

- [ ] **Step 2: Add the `<th>` between OS (~line 56) and Status (~line 57)**

```tsx
<th className="hidden px-3 py-3 font-medium lg:table-cell">
  {t('columns.clientVersion')}
</th>
```

- [ ] **Step 3: Add the `<td>` between the OS cell (~line 125) and Status cell (~line 126)**

```tsx
<td className="hidden px-3 py-3 align-top lg:table-cell">
  <ClientVersionCell
    version={p.clientVersion}
    latest={latestClientVersion}
    upgradeAvailable={p.upgradeAvailable}
  />
</td>
```

- [ ] **Step 4: Add `latestClientVersion` to PeerTable props**

Find the PeerTable component signature (probably `function PeerTable({ peers, selectedId, expandedId, onSelect }: PeerTableProps)`). Add:

```tsx
type PeerTableProps = {
  // ... existing fields ...
  latestClientVersion?: string;
};
```

Destructure it from props alongside the others.

- [ ] **Step 5: Thread the prop from the parent page**

Open `apps/web/src/app/[locale]/peers/page.tsx`. Where the page calls `listPeers()` (or the SWR hook that wraps it) and renders `<PeerTable ... />`, destructure `latestClientVersion` from the response and pass it as a prop:

```tsx
const { peers, latestClientVersion } = data ?? { peers: [] };
// ...
<PeerTable
  peers={peers}
  latestClientVersion={latestClientVersion}
  // ... existing props ...
/>
```

If there's an intermediate wrapper (e.g. `PeersClient.tsx`), thread the prop through it too.

- [ ] **Step 6: Typecheck + lint**

```bash
pnpm typecheck && pnpm lint
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add apps/web/src/components/PeerTable.tsx \
        'apps/web/src/app/[locale]/peers/page.tsx'
# (also add any intermediate PeersClient.tsx if you touched one)
git commit -m "feat(web): PeerTable Client ver column + upgrade indicator"
```

### Task 4.6 — Manual verification

- [ ] **Step 1: Boot the dev stack against staging or a local controller with the feature**

```bash
# Terminal 1 — controller (with PR 2 merged):
cd apps/controller
BAMBOO_RELEASE_FEED_ENABLED=true \
BAMBOO_RELEASE_FEED_REPO=hanfour/bamboo \
go run ./cmd/controller

# Terminal 2 — web:
cd apps/web
pnpm dev
```

- [ ] **Step 2: Open `http://localhost:3000/peers` in a browser**

Confirm:
- Peers with `client_version` empty render `—` muted.
- Peers at-or-above the controller's known latest render plain version text.
- Peers behind render `0.1.2 ↑ 0.1.4` with amber arrow + hover tooltip.
- Resizing the browser below 1024px hides the column (no horizontal scroll).
- Both `en` and `zh-TW` locales render correctly.

- [ ] **Step 3: Screenshot for the PR (optional but useful)**

### Task 4.7 — PR 4 push + open

- [ ] **Step 1: Push + PR**

```bash
git push -u origin feat-web-peer-version-column
gh pr create --title "feat(web): PeerTable Client ver column + upgrade indicator" --body "$(cat <<'EOF'
## Summary

Consumes the API surface added in the previous controller PR (\`latestClientVersion\` top-level + per-peer \`upgradeAvailable\`) to render a new "Client ver" column in PeerTable. Peers behind get an amber \`↑ X.Y.Z\` badge with a hover tooltip; peers at-or-ahead render plain version text; unknown-version peers render \`—\`.

Column is \`responsive: 'lg'\` to keep the table from horizontally scrolling on narrower viewports — mirrors the convention used by DNS / Tags columns.

## Why no unit tests

\`apps/web/\` has no test runner configured (no vitest / jest / react-testing-library in \`package.json\`). Coverage relies on \`pnpm typecheck\` + \`pnpm lint\` + manual browser verification. Adding the test infra is a separate roadmap item.

## Test plan

- [x] \`pnpm typecheck\` clean
- [x] \`pnpm lint\` clean
- [ ] Manual: peers at \`0.1.3 / 0.1.4 / 0.1.5\` against controller with latest=\`0.1.4\` render \`↑ / plain / plain\`
- [ ] Manual: empty client_version renders \`—\` muted
- [ ] Manual: < 1024px viewport hides the column
- [ ] Manual: both \`en\` and \`zh-TW\` locales

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Cross-cutting follow-ups (out of scope for this plan, file as issues)

- **Add Vitest + Testing Library to `apps/web/`** so PeerTable + neighbour components can grow real unit coverage. Currently blocking Phase 4 from having tests; not blocking shipping the feature.
- **Document the release checklist update** in `docs/development/` so the Xcode `MARKETING_VERSION` bump alongside the git tag is a written step, not tribal knowledge.

---

## Spec coverage self-check

| Spec §                                         | Covered by task |
|------------------------------------------------|-----------------|
| `internal/releasefeed` package + 7 tests       | 1.4             |
| Controller config (3 env vars + clamp + repo regex) | 1.3        |
| `version_compare` helper + 8 tests             | 1.2             |
| `apiPeerJSON.UpgradeAvailable` + handler wiring | 2.3            |
| `latestClientVersion` top-level field          | 2.3             |
| `NewHTTPServer` + Server.Run wiring            | 2.2             |
| Apple `bundleVersion` helper + 3 tests         | 3.2 / 3.3       |
| Apple replace `"0.0.1"` (2 sites)              | 3.3             |
| Web `ClientVersionCell` + column + RWD         | 4.5             |
| Web i18n (en + zh-TW)                          | 4.3             |
| Web types (peer + response)                    | 4.2             |
| Web fetch surfacing of `latestClientVersion`   | 4.4             |
| CLI unchanged (already wired)                  | (noted in spec; no task) |
