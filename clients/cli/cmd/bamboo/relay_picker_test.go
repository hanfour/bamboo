// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// TestPickRelay_EmptyList pins the "no candidates" base case:
// caller passes [], picker returns nil + nil so maybeOpenRelay
// degrades to direct-only without ever firing a probe goroutine.
func TestPickRelay_EmptyList(t *testing.T) {
	got, err := pickRelay(context.Background(), nil)
	if got != nil || err != nil {
		t.Errorf("pickRelay(nil) = (%v, %v), want (nil, nil)", got, err)
	}
}

// TestPickRelay_SelectsLowestRTT uses the probeFn seam to feed a
// deterministic per-hostname latency table to the picker, then
// asserts the lowest-RTT relay wins. A real TCP-connect test
// can't reliably tune actual connect latencies on localhost
// (kernel completes the handshake before any user-space accept
// logic runs), so we exercise the ranking logic in isolation.
//
// Without this test, a refactor that sorts the wrong direction
// (DESC vs ASC) would silently pick the slowest relay every time
// while every other test in the suite stays green.
func TestPickRelay_SelectsLowestRTT(t *testing.T) {
	orig := probeFn
	probeFn = func(_ context.Context, host string, _ int) (time.Duration, error) {
		switch host {
		case "fast.example.com":
			return 5 * time.Millisecond, nil
		case "medium.example.com":
			return 50 * time.Millisecond, nil
		case "slow.example.com":
			return 200 * time.Millisecond, nil
		}
		return 0, fmt.Errorf("unknown host %s", host)
	}
	defer func() { probeFn = orig }()

	candidates := []*bamboov1.RelayServer{
		{Region: "slow", Hostname: "slow.example.com", Port: 8443},
		{Region: "fast", Hostname: "fast.example.com", Port: 8443},
		{Region: "medium", Hostname: "medium.example.com", Port: 8443},
	}
	got, err := pickRelay(context.Background(), candidates)
	if err != nil {
		t.Fatalf("pickRelay: %v", err)
	}
	if got == nil {
		t.Fatal("pickRelay returned nil")
	}
	if got.GetRegion() != "fast" {
		t.Errorf("picked region = %q, want fast", got.GetRegion())
	}
}

// TestPickRelay_AllFailReturnsError pins the failure-aggregation
// path: every probe failing must surface as a non-nil error (so
// maybeOpenRelay logs the cause) but must NOT crash. The caller
// then falls back to direct-only.
func TestPickRelay_AllFailReturnsError(t *testing.T) {
	// Two unbound ports — connection refused.
	deadA, deadB := unboundPort(t), unboundPort(t)
	candidates := []*bamboov1.RelayServer{
		{Region: "deadA", Hostname: "127.0.0.1", Port: int32(deadA)},
		{Region: "deadB", Hostname: "127.0.0.1", Port: int32(deadB)},
	}
	got, err := pickRelay(context.Background(), candidates)
	if got != nil {
		t.Errorf("picked = %v, want nil when all probes fail", got)
	}
	if err == nil {
		t.Errorf("err = nil, want a joined error so the caller can log the cause")
	}
}

// TestPickRelay_HealthyAmongFailedWins pins the partial-failure
// path: with one bad relay and one good relay, the picker MUST
// return the good one even though the bad one's probe error
// would otherwise dominate the aggregated error message.
//
// This is the production-relevant case — controller's stage 1
// reaper already removed unhealthy rows, but a probe can still
// fail transiently (network blip between client and one region).
func TestPickRelay_HealthyAmongFailedWins(t *testing.T) {
	deadPort := unboundPort(t)
	goodAddr := newSyntheticRelay(t)
	candidates := []*bamboov1.RelayServer{
		{Region: "dead", Hostname: "127.0.0.1", Port: int32(deadPort)},
		buildRelay("good", goodAddr),
	}
	got, err := pickRelay(context.Background(), candidates)
	if err != nil {
		t.Errorf("err = %v, want nil when at least one probe succeeds", err)
	}
	if got == nil || got.GetRegion() != "good" {
		t.Errorf("picked = %v, want good", got)
	}
}

// TestPickRelay_RespectsTimeout pins the probe deadline: a relay
// that accepts the TCP connect but then hangs (the "half-up"
// failure mode) must time out at relayProbeTimeout. Otherwise
// one bad relay can stall tunnel bring-up by holding the picker
// goroutine indefinitely.
//
// We use the "unbound port → no SYN-ACK" pattern to simulate the
// hang, since httptest doesn't easily reproduce a wedged accept
// queue.
func TestPickRelay_RespectsTimeout(t *testing.T) {
	// Use a non-routable address — TCP SYN goes into the void.
	// 192.0.2.0/24 is reserved for documentation (RFC 5737) and
	// never routes anywhere.
	orig := relayProbeTimeout
	relayProbeTimeout = 100 * time.Millisecond
	defer func() { relayProbeTimeout = orig }()

	candidates := []*bamboov1.RelayServer{
		{Region: "void", Hostname: "192.0.2.1", Port: 8443},
	}
	start := time.Now()
	got, err := pickRelay(context.Background(), candidates)
	elapsed := time.Since(start)
	if got != nil || err == nil {
		t.Errorf("pickRelay = (%v, %v), want (nil, err)", got, err)
	}
	// 1s ceiling — anything longer means the per-probe timeout
	// didn't trip and we'd block the user's tunnel bring-up.
	if elapsed > time.Second {
		t.Errorf("pickRelay took %v, want under 1s with 100ms probe timeout", elapsed)
	}
}

func TestRelayWSSURL(t *testing.T) {
	rs := &bamboov1.RelayServer{Hostname: "relay-east.example.com", Port: 8443}
	got := relayWSSURL(rs)
	want := "wss://relay-east.example.com:8443/relay"
	if got != want {
		t.Errorf("relayWSSURL = %q, want %q", got, want)
	}
}

func TestJoinErrors(t *testing.T) {
	if got := joinErrors(nil); got != nil {
		t.Errorf("joinErrors(nil) = %v, want nil", got)
	}
	single := joinErrors([]error{fmt.Errorf("boom")})
	if single == nil || single.Error() != "boom" {
		t.Errorf("single-error case = %v, want unwrapped 'boom'", single)
	}
	multi := joinErrors([]error{fmt.Errorf("a"), fmt.Errorf("b")})
	if multi == nil || !strings.Contains(multi.Error(), "[a]") || !strings.Contains(multi.Error(), "[b]") {
		t.Errorf("multi-error case = %v, want both messages bracketed", multi)
	}
}

// newSyntheticRelay binds a TCP listener that accepts and then
// closes connections. Used by tests that need a real-IP relay
// the picker's TCP-connect probe can dial successfully — RTT
// values aren't meaningful here (kernel handles connect before
// any accept logic runs), so the ordering test uses the probeFn
// seam instead.
func newSyntheticRelay(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				_ = c.Close()
			}(conn)
		}
	}()
	t.Cleanup(wg.Wait)
	return ln.Addr().String()
}

// buildRelay turns a "host:port" address into the proto RelayServer
// shape the picker consumes. region is the test label so failed
// assertions point at "which relay won/lost".
func buildRelay(region, addr string) *bamboov1.RelayServer {
	host, port, _ := net.SplitHostPort(addr)
	var p int32
	_, _ = fmt.Sscanf(port, "%d", &p)
	return &bamboov1.RelayServer{Region: region, Hostname: host, Port: p}
}

func unboundPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
