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

// relayRegionToleranceMs is the §4 P2 stage 5 affinity window. A
// same-region relay wins over a lower-RTT out-of-region relay when
// the RTT gap is within this many milliseconds — the same magnitude
// as typical intra-AZ jitter, so we're not preferring slowness for
// the sake of geography. Beyond the tolerance the cross-region
// option is genuinely faster and wins.
//
// 50ms picked from observation: trans-continental RTT (say
// us-east-1 ↔ ap-northeast-1) is 150-200ms; intra-region jitter is
// typically <20ms. A 50ms tolerance absorbs the jitter without ever
// letting a far-away relay win when it shouldn't.
var relayRegionToleranceMs = int64(50)

// probeFn is the seam tests use to swap in a deterministic latency
// table for pickRelay's ranking logic. Production binds it to
// probeRelayRTT at init; tests overwrite with a closure that maps
// hostname → simulated RTT. Kept as a package var rather than a
// parameter on pickRelay to keep the call site in maybeOpenRelay
// quiet — only tests care about the seam.
var probeFn = probeRelayRTT

// rankedRelay is an internal record of (candidate index, measured
// RTT) used to rank probes. Hoisted to file scope so the region-
// affinity helper can take a slice of it without re-declaring an
// anonymous structurally-equivalent type that wouldn't be
// assignable across function boundaries.
type rankedRelay struct {
	idx int
	rtt time.Duration
}

// pickRelay selects the lowest-RTT relay from a controller-supplied
// list (§4 P2 multi-relay stage 2). RTT is measured as TCP connect
// time to (hostname, port) — same 5-tuple a real relay session
// would use, so a stuck firewall on the relay port surfaces here
// too.
//
// preferredRegion is the §4 P2 stage 5 affinity hint. Empty ⇒ pure
// RTT ranking (back-compatible with pre-stage-5 behaviour). When
// set, a same-region relay wins over a lower-RTT out-of-region
// relay if the RTT gap is within relayRegionToleranceMs. The point
// is to stop the picker flapping between two regions whose RTT
// numbers happen to land close — pick a "home" and stay there.
//
// Returns nil + nil when the list is empty (caller falls back to
// the env-var override path). Returns nil + the joined error when
// every probe fails — the caller treats that as "no usable relay"
// and continues without one rather than blocking tunnel bring-up.
//
// Parallel probes by design: serial would stretch a 5-relay probe
// to 5s in the worst case, doubling the user's perceived bring-up
// latency. The contention overhead at this fan-out is negligible.
func pickRelay(ctx context.Context, candidates []*bamboov1.RelayServer, preferredRegion string) (*bamboov1.RelayServer, error) {
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

	var healthy []rankedRelay
	var errs []error
	for r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", candidates[r.idx].GetHostname(), r.err))
			continue
		}
		healthy = append(healthy, rankedRelay{idx: r.idx, rtt: r.rtt})
	}
	if len(healthy) == 0 {
		return nil, joinErrors(errs)
	}
	sort.SliceStable(healthy, func(a, b int) bool { return healthy[a].rtt < healthy[b].rtt })
	winnerIdx := applyRegionAffinity(candidates, healthy[0].idx, healthy[0].rtt, healthy, preferredRegion)
	return candidates[winnerIdx], nil
}

// applyRegionAffinity returns the index of the winning relay after
// the §4 P2 stage 5 same-region preference. preferredRegion empty
// ⇒ the RTT winner stays. Otherwise: if the RTT winner already is
// in the preferred region, no-op. If a same-region relay exists
// within toleranceMs of the RTT winner's latency, prefer it
// (lowest-RTT among same-region rows wins the tiebreaker — sort
// order from the caller is already ASC, so the first match wins).
//
// Returns the original winnerIdx when no eligible same-region
// candidate is found, so a region hint can never make the picker
// worse — only same or better, never slower than tolerance.
func applyRegionAffinity(
	candidates []*bamboov1.RelayServer,
	winnerIdx int,
	winnerRTT time.Duration,
	healthy []rankedRelay,
	preferredRegion string,
) int {
	if preferredRegion == "" {
		return winnerIdx
	}
	if candidates[winnerIdx].GetRegion() == preferredRegion {
		return winnerIdx
	}
	tolerance := time.Duration(relayRegionToleranceMs) * time.Millisecond
	for _, r := range healthy {
		if candidates[r.idx].GetRegion() != preferredRegion {
			continue
		}
		if r.rtt-winnerRTT <= tolerance {
			return r.idx
		}
		// healthy is ASC by RTT; once a same-region row exceeds
		// the tolerance, all later ones do too. Break.
		break
	}
	return winnerIdx
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
