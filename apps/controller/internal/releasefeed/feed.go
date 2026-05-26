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
	"io"
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
// updates the cached state. Called only by Run in production;
// tests in the same package call it directly to drive specific
// sequences (failure / success / malformed-body) without waiting
// on Run's ticker.
func (f *Feed) fetchOnce(ctx context.Context) error {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", f.baseURL, f.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return f.recordFailure(err)
	}
	req.Header.Set("User-Agent", "bamboo-controller")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
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
	f.consecutiveFailures = 0
}

func (f *Feed) recordFailure(err error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutiveFailures++
	return err
}
