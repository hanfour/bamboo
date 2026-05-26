// SPDX-License-Identifier: AGPL-3.0-or-later

package releasefeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	var failNext atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext.Load() {
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
	failNext.Store(true)
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
// test after timeout.
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
