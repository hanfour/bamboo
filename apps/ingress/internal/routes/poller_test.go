// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeToken(t *testing.T, tok string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ingress.token")
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type fakeController struct {
	status    atomic.Int32 // 0 => 200 with body
	sawAuth   atomic.Value
	sawIfNone atomic.Value
	routes    []Route
	rawBody   atomic.Value // string; when set, served instead of routes
}

func (f *fakeController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.sawAuth.Store(r.Header.Get("Authorization"))
	f.sawIfNone.Store(r.Header.Get("If-None-Match"))
	if r.URL.Path != "/internal/v1/ingress/routes" {
		http.NotFound(w, r)
		return
	}
	if s := f.status.Load(); s != 0 {
		w.WriteHeader(int(s))
		return
	}
	if r.Header.Get("If-None-Match") == `"v1"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", `"v1"`)
	if raw, ok := f.rawBody.Load().(string); ok {
		_, _ = w.Write([]byte(raw))
		return
	}
	_ = json.NewEncoder(w).Encode(f.routes)
}

func newTestPoller(t *testing.T, url string) (*Poller, *Table) {
	tb := NewTable("svc.example.test")
	return NewPoller(PollerOptions{ControllerURL: url, TokenPath: writeToken(t, "tok-1"), Table: tb}), tb
}

func oneRoute() []Route {
	return []Route{{ServiceID: "s1", Label: "app", PeerIP: "100.64.0.5", TargetPort: 47800}}
}

func TestPoller_LoadsRoutesWithToken(t *testing.T) {
	fc := &fakeController{routes: oneRoute()}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	p, tb := newTestPoller(t, srv.URL)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := fc.sawAuth.Load(); got != "Bearer tok-1" {
		t.Errorf("Authorization = %v", got)
	}
	if _, ok := tb.Lookup("app.svc.example.test"); !ok {
		t.Error("route not installed")
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if got := fc.sawIfNone.Load(); got != `"v1"` {
		t.Errorf("If-None-Match = %v, want \"v1\"", got)
	}
	if tb.Len() != 1 {
		t.Error("304 must keep the table")
	}
}

func TestPoller_KeepsLastTableOnErrors(t *testing.T) {
	fc := &fakeController{routes: oneRoute()}
	srv := httptest.NewServer(fc)
	p, tb := newTestPoller(t, srv.URL)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.status.Store(http.StatusInternalServerError)
	if err := p.Refresh(context.Background()); err == nil {
		t.Error("500 must be an error")
	}
	srv.Close()
	if err := p.Refresh(context.Background()); err == nil {
		t.Error("unreachable controller must be an error")
	}
	if _, ok := tb.Lookup("app.svc.example.test"); !ok {
		t.Error("last good table must survive controller errors")
	}
}

func TestPoller_KeepsLastTableOnBadPayload(t *testing.T) {
	good := `[{"service_id":"s1","label":"app","peer_ip":"100.64.0.5","target_port":47800}]`
	bad := map[string]string{
		"not json":        `<html>oops</html>`,
		"truncated":       `[{"service_id":"s1"`,
		"null":            `null`,
		"object":          `{"routes":[]}`,
		"trailing data":   good + `{}`,
		"invalid ip":      `[{"service_id":"s2","label":"x","peer_ip":"nope","target_port":1}]`,
		"invalid port":    `[{"service_id":"s2","label":"x","peer_ip":"100.64.0.6","target_port":70000}]`,
		"invalid label":   `[{"service_id":"s2","label":"A.B","peer_ip":"100.64.0.6","target_port":1}]`,
		"missing id":      `[{"label":"x","peer_ip":"100.64.0.6","target_port":1}]`,
		"duplicate label": `[{"service_id":"a","label":"x","peer_ip":"100.64.0.6","target_port":1},{"service_id":"b","label":"x","peer_ip":"100.64.0.7","target_port":1}]`,
		"one bad of two":  `[{"service_id":"a","label":"x","peer_ip":"100.64.0.6","target_port":1},{"service_id":"b","label":"y","peer_ip":"","target_port":1}]`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			fc := &fakeController{routes: oneRoute()}
			srv := httptest.NewServer(fc)
			defer srv.Close()
			p, tb := newTestPoller(t, srv.URL)
			if err := p.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			p.etag = "" // force a full response
			fc.rawBody.Store(body)
			if err := p.Refresh(context.Background()); err == nil {
				t.Error("bad payload must be an error")
			}
			if tb.Len() != 1 {
				t.Errorf("table changed: Len = %d", tb.Len())
			}
			if _, ok := tb.Lookup("app.svc.example.test"); !ok {
				t.Error("last good table lost")
			}
			// A rejected payload must not poison the ETag: after the
			// controller recovers the next poll still gets a full body.
			if p.etag != `"v1"` && p.etag != "" {
				t.Errorf("unexpected etag %q", p.etag)
			}
		})
	}
}

func TestPoller_EmptyArrayClearsTable(t *testing.T) {
	fc := &fakeController{routes: oneRoute()}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	p, tb := newTestPoller(t, srv.URL)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.etag = ""
	fc.rawBody.Store(`[]`)
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tb.Len() != 0 {
		t.Error("an explicit empty list means no live services")
	}
}

func TestPoller_EmptyTokenIsAnError(t *testing.T) {
	tb := NewTable("svc.example.test")
	p := NewPoller(PollerOptions{ControllerURL: "http://127.0.0.1:1", TokenPath: writeToken(t, "  "), Table: tb})
	if err := p.Refresh(context.Background()); err == nil {
		t.Error("empty token file must fail")
	}
}

func TestPoller_MissingTokenFileIsAnError(t *testing.T) {
	tb := NewTable("svc.example.test")
	p := NewPoller(PollerOptions{ControllerURL: "http://127.0.0.1:1", TokenPath: filepath.Join(t.TempDir(), "nope"), Table: tb})
	if err := p.Refresh(context.Background()); err == nil {
		t.Error("missing token file must fail")
	}
}

func TestPoller_NeverLogsToken(t *testing.T) {
	const secret = "SECRET-TOKEN-VALUE"
	fc := &fakeController{routes: oneRoute()}
	fc.status.Store(http.StatusUnauthorized)
	srv := httptest.NewServer(fc)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var gotErr error
	tb := NewTable("svc.example.test")
	p := NewPoller(PollerOptions{
		ControllerURL: srv.URL, TokenPath: writeToken(t, secret), Table: tb, Log: log,
		Interval: 10 * time.Millisecond,
		OnResult: func(_ int, err error) {
			if err != nil {
				gotErr = err
			}
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	p.Run(ctx) // 401 path
	srv.Close()
	p.Run(ctx2(t, 40*time.Millisecond)) // unreachable path
	if gotErr == nil {
		t.Fatal("expected errors")
	}
	if buf.Len() == 0 {
		t.Fatal("expected the failure to be logged")
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(gotErr.Error(), secret) {
		t.Errorf("token leaked: %s / %v", buf.String(), gotErr)
	}
}

func ctx2(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestPoller_RunStopsOnCancelWithoutLeak(t *testing.T) {
	fc := &fakeController{routes: oneRoute()}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	srv.Client().CloseIdleConnections()
	before := runtime.NumGoroutine()

	tb := NewTable("svc.example.test")
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	p := NewPoller(PollerOptions{ControllerURL: srv.URL, TokenPath: writeToken(t, "tok"), Table: tb, Interval: 5 * time.Millisecond, Client: client})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for tb.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if tb.Len() == 0 {
		t.Fatal("poller never loaded the table")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	deadline = time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: before %d, after %d", before, after)
	}
}

func TestPoller_RunPicksUpChangesAndRecovers(t *testing.T) {
	fc := &fakeController{routes: oneRoute()}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	tb := NewTable("svc.example.test")
	p := NewPoller(PollerOptions{ControllerURL: srv.URL, TokenPath: writeToken(t, "tok"), Table: tb, Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	wait := func(cond func() bool) {
		t.Helper()
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatal("condition not reached")
	}
	wait(func() bool { return tb.Len() == 1 })
	fc.status.Store(http.StatusBadGateway)
	time.Sleep(30 * time.Millisecond)
	if tb.Len() != 1 {
		t.Fatal("table must survive controller failure while polling")
	}
	fc.status.Store(0)
	fc.rawBody.Store(`[]`)
	// The stored ETag is "v1" and the fake answers 304 for it, so the table
	// must stay intact after recovery.
	time.Sleep(30 * time.Millisecond)
	if tb.Len() != 1 {
		t.Fatal("304 after recovery must keep the table")
	}
}

// TestPoller_ClearsTableWhenTokenRejected: 401/403 means the controller
// revoked this ingress (token rotated, expired, peer untagged). Keeping
// the last table would keep serving services the operator turned off, so
// the table and the ETag are dropped; the error still surfaces.
func TestPoller_ClearsTableWhenTokenRejected(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		fc := &fakeController{routes: oneRoute()}
		srv := httptest.NewServer(fc)
		p, tb := newTestPoller(t, srv.URL)
		if err := p.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		fc.status.Store(int32(code))
		if err := p.Refresh(context.Background()); err == nil {
			t.Errorf("%d must be an error", code)
		}
		if tb.Len() != 0 {
			t.Errorf("%d: table has %d routes, want cleared", code, tb.Len())
		}
		fc.status.Store(0)
		if err := p.Refresh(context.Background()); err != nil {
			t.Fatalf("%d: recover: %v", code, err)
		}
		if got := fc.sawIfNone.Load(); got != "" {
			t.Errorf("%d: If-None-Match after clear = %v, want none", code, got)
		}
		if tb.Len() != 1 {
			t.Errorf("%d: table not reloaded after recovery", code)
		}
		srv.Close()
	}
}
