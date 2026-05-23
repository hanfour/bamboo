// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestStreamWatchEvents_EmitsKeepalive verifies the SSE handler
// writes a `: keepalive\n\n` comment frame at the configured
// interval. The Swift PeerWatcher parser ignores lines that don't
// match `event: ` / `data: ` prefixes, so comment lines are safe
// payload but the bytes-on-the-wire activity keeps HTTP/2 idle
// timers from tearing the stream down between real events.
func TestStreamWatchEvents_EmitsKeepalive(t *testing.T) {
	rec := httptest.NewRecorder()
	ch := make(chan *bamboov1.WatchPeersEvent)
	ctx, cancel := context.WithCancel(context.Background())

	// Run streamWatchEvents in the background; cancel after enough
	// time for at least 3 keepalive ticks at 30ms interval (~90ms).
	done := make(chan struct{})
	go func() {
		streamWatchEvents(ctx, rec, rec, ch, 30*time.Millisecond)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	count := strings.Count(body, ": keepalive\n\n")
	if count < 2 {
		t.Errorf("expected at least 2 keepalive frames in 120ms with 30ms interval; got %d.\nbody=%q", count, body)
	}
	// And nothing else — no events fed, so no `event:` / `data:`
	// lines should be in the output.
	if strings.Contains(body, "event:") || strings.Contains(body, "data:") {
		t.Errorf("expected only keepalive frames; saw event/data lines:\n%s", body)
	}
}

// TestStreamWatchEvents_EmitsEventThenKeepalive verifies that real
// events and keepalive frames interleave correctly — a real event
// doesn't reset the keepalive ticker (so a chatty stream still emits
// keepalives, useful as a heartbeat for the client to detect stuck
// connections).
func TestStreamWatchEvents_EmitsEventThenKeepalive(t *testing.T) {
	rec := httptest.NewRecorder()
	ch := make(chan *bamboov1.WatchPeersEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())

	// Send one real event right away.
	ch <- &bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PolicyChanged{
			PolicyChanged: &bamboov1.PolicyChanged{PolicyRevision: 42},
		},
	}

	done := make(chan struct{})
	go func() {
		streamWatchEvents(ctx, rec, rec, ch, 30*time.Millisecond)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, "event: policy_changed\n") {
		t.Errorf("expected the policy_changed event in body; got:\n%s", body)
	}
	if !strings.Contains(body, "\"policyRevision\":42") {
		t.Errorf("expected policy_changed payload to contain policyRevision=42; got:\n%s", body)
	}
	if strings.Count(body, ": keepalive\n\n") < 1 {
		t.Errorf("expected at least one keepalive frame alongside the event; got:\n%s", body)
	}
}

// TestStreamWatchEvents_ContextCancelEndsLoop verifies that cancelling
// the request context closes the handler cleanly without a final
// keepalive write (so a closing connection doesn't get an extra
// frame queued behind it).
func TestStreamWatchEvents_ContextCancelEndsLoop(t *testing.T) {
	rec := httptest.NewRecorder()
	ch := make(chan *bamboov1.WatchPeersEvent)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		streamWatchEvents(ctx, rec, rec, ch, time.Hour) // never tick
		close(done)
	}()

	// Cancel before any keepalive could fire.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("streamWatchEvents didn't return within 1s of cancel")
	}

	if rec.Body.Len() != 0 {
		t.Errorf("expected zero bytes written on cancel-before-tick; got %d: %q",
			rec.Body.Len(), rec.Body.String())
	}
}

// httptest.ResponseRecorder doesn't implement http.Flusher by default,
// but our streamWatchEvents takes Flusher separately so the recorder
// can also act as the flusher. ResponseRecorder satisfies the
// interface via its Flush() method (introduced in Go 1.19+); confirm
// at compile time.
var _ http.Flusher = (*httptest.ResponseRecorder)(nil)
