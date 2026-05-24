// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// relayProbeTimeout caps each per-relay TCP-connect probe. Tighter
// than the controller's 3s /healthz probe because here we're racing
// to bring the tunnel up — every second spent probing is a second
// the user sees nothing. 1s is enough to fingerprint inter-region
// latency (typical cross-region is 50-300ms, intra-region <30ms);
// anything slower is already too slow to be the preferred relay.
var relayProbeTimeout = 1 * time.Second

// probeFn is the seam tests use to swap in a deterministic latency
// table for pickRelay's ranking logic. Production binds it to
// probeRelayRTT at init; tests overwrite with a closure that maps
// hostname → simulated RTT. Kept as a package var rather than a
// parameter on pickRelay to keep the call site in maybeOpenRelay
// quiet — only tests care about the seam.
var probeFn = probeRelayRTT

// pickRelay selects the lowest-RTT relay from a controller-supplied
// list (§4 P2 multi-relay stage 2). RTT is measured as TCP connect
// time to (hostname, port) — same 5-tuple a real relay session
// would use, so a stuck firewall on the relay port surfaces here
// too.
//
// Returns nil + nil when the list is empty (caller falls back to
// the env-var override path). Returns nil + the joined error when
// every probe fails — the caller treats that as "no usable relay"
// and continues without one rather than blocking tunnel bring-up.
//
// Parallel probes by design: serial would stretch a 5-relay probe
// to 5s in the worst case, doubling the user's perceived bring-up
// latency. The contention overhead at this fan-out is negligible.
func pickRelay(ctx context.Context, candidates []*bamboov1.RelayServer) (*bamboov1.RelayServer, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	type result struct {
		idx int
		rtt time.Duration
		err error
	}
	results := make(chan result, len(candidates))
	var wg sync.WaitGroup
	wg.Add(len(candidates))
	for i, rs := range candidates {
		go func(idx int, hostname string, port int32) {
			defer wg.Done()
			rtt, err := probeFn(ctx, hostname, int(port))
			results <- result{idx: idx, rtt: rtt, err: err}
		}(i, rs.GetHostname(), rs.GetPort())
	}
	wg.Wait()
	close(results)

	type ranked struct {
		idx int
		rtt time.Duration
	}
	var healthy []ranked
	var errs []error
	for r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", candidates[r.idx].GetHostname(), r.err))
			continue
		}
		healthy = append(healthy, ranked{idx: r.idx, rtt: r.rtt})
	}
	if len(healthy) == 0 {
		return nil, joinErrors(errs)
	}
	sort.SliceStable(healthy, func(a, b int) bool { return healthy[a].rtt < healthy[b].rtt })
	return candidates[healthy[0].idx], nil
}

// probeRelayRTT measures TCP-connect latency to one relay endpoint.
// Closed immediately after measurement — we only care about the
// dial; reading bytes would add a TLS handshake's worth of variance
// that doesn't reflect what matters (will my WG-over-WSS session
// land here quickly).
//
// Returns the measured RTT on success. On failure (timeout, refused,
// DNS fail) returns 0 + the underlying error so the caller can log
// what's wrong with which relay.
func probeRelayRTT(ctx context.Context, hostname string, port int) (time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, relayProbeTimeout)
	defer cancel()
	dialer := &net.Dialer{}
	start := time.Now()
	conn, err := dialer.DialContext(probeCtx, "tcp", fmt.Sprintf("%s:%d", hostname, port))
	rtt := time.Since(start)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return rtt, nil
}

// relayWSSURL builds the wss URL for a chosen relay. The controller's
// shape stores (hostname, port) — the /relay path + wss:// scheme
// are convention, not data. Pulled out so a future change to the
// scheme (e.g. h3) lives in one place instead of being threaded
// through every dial site.
func relayWSSURL(rs *bamboov1.RelayServer) string {
	return fmt.Sprintf("wss://%s:%d/relay", rs.GetHostname(), rs.GetPort())
}

// joinErrors collapses a slice of probe errors into one error
// suitable for logging. Returns nil when the slice is empty.
// Single-error case is unwrapped so the caller sees the bare cause
// rather than a "[...]" wrapper.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msg := "all relay probes failed:"
	for _, e := range errs {
		msg += " [" + e.Error() + "]"
	}
	return fmt.Errorf("%s", msg)
}
