// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	stdsync "sync"
	"time"

	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
)

// CandidateProbeTimeout is how long a peer may sit on one candidate
// endpoint without a handshake before the rotator moves it to the
// next. WireGuard retries an unanswered initiation every 5s, so 20s
// gives a candidate four attempts.
const CandidateProbeTimeout = 20 * time.Second

// HandshakeFreshness is the age under which a handshake counts as
// live. It matches WireGuard's REJECT_AFTER_TIME: a session older than
// this carries no traffic.
const HandshakeFreshness = 180 * time.Second

// RotationInterval is how often the daemon checks for peers to rotate.
// Short against CandidateProbeTimeout so a switch lands within a
// couple of seconds of the deadline.
const RotationInterval = 2 * time.Second

// candidateBackoff is the pause after every candidate failed once,
// before the cycle restarts at the first one. The last entry repeats.
var candidateBackoff = []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second}

// CandidateRotator chooses each peer's direct endpoint from the
// controller's candidates and moves on when one stays silent.
//
// Selection: wg.UsableCandidates drops private candidates that are not
// on a local subnet and puts same-LAN ones first.
//
// Rotation: a peer with no live handshake CandidateProbeTimeout after
// a candidate was assigned moves to the next; after the last it waits
// candidateBackoff and restarts at the first. A peer stops rotating
// for good, until the controller sends a different candidate list,
// once either of these holds:
//   - it has a live handshake. If WireGuard roamed to an endpoint it
//     learned from the peer, that endpoint is kept: the rotator's
//     intent no longer changes, and the device leaves an endpoint
//     alone while the intent is unchanged (#309).
//   - the relay fallback moved it to a relay proxy. The relay owns the
//     endpoint from then on; rotation would only fight it.
//
// With the default timings a peer with two dead candidates is still
// probing (or backing off) when RunRelayFallback's 60s threshold
// fires, so the relay takes over and rotation stops for it.
type CandidateRotator struct {
	mu    stdsync.Mutex
	local func() []netip.Prefix
	now   func() time.Time
	peers map[string]rotation
}

type rotation struct {
	candidates []string // controller list, compared to detect changes
	usable     []string
	index      int
	since      time.Time // when usable[index] was assigned
	retryAt    time.Time // non-zero while backing off
	failures   int
	stopped    bool
}

// NewCandidateRotator returns a rotator that reads this host's subnets
// from local and the time from now. Both are injected for tests.
func NewCandidateRotator(local func() []netip.Prefix, now func() time.Time) *CandidateRotator {
	return &CandidateRotator{local: local, now: now, peers: make(map[string]rotation)}
}

// Select returns a copy of cfg with each peer's Endpoint set to its
// current candidate. A peer whose candidate list differs from the last
// call starts over at its first usable candidate; unchanged peers keep
// their rotation state, so calling Select again with the same config
// returns the same endpoints. Peers absent from cfg are forgotten.
func (r *CandidateRotator) Select(cfg *wg.DeviceConfig) *wg.DeviceConfig {
	if cfg == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := *cfg
	out.Peers = slices.Clone(cfg.Peers)
	next := make(map[string]rotation, len(out.Peers))
	for i := range out.Peers {
		p := &out.Peers[i]
		key := p.PublicKey.Base64()
		st, ok := r.peers[key]
		if !ok || !slices.Equal(st.candidates, p.Candidates) {
			st = newRotation(p.Candidates, r.local(), now)
		}
		next[key] = st
		if ep := st.current(); ep != "" {
			p.Endpoint = ep
		}
	}
	r.peers = next
	return &out
}

// Observe advances rotation from a device stats snapshot. relayed holds
// the peers currently on a relay override. It reports whether any
// peer's endpoint changed; the caller then re-applies Select's output.
func (r *CandidateRotator) Observe(stats []device.PeerStats, relayed map[string]string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	changed := false
	for _, s := range stats {
		st, ok := r.peers[s.PublicKey]
		if !ok {
			continue
		}
		_, onRelay := relayed[s.PublicKey]
		next := st.step(now, s, onRelay)
		if next.current() != st.current() {
			slog.Info("endpoint rotation", "peer", shortKey(s.PublicKey), "from", st.current(), "to", next.current())
			changed = true
		}
		r.peers[s.PublicKey] = next
	}
	return changed
}

func newRotation(candidates []string, local []netip.Prefix, now time.Time) rotation {
	return rotation{
		candidates: slices.Clone(candidates),
		usable:     wg.UsableCandidates(candidates, local),
		since:      now,
	}
}

func (s rotation) current() string {
	if len(s.usable) == 0 {
		return ""
	}
	return s.usable[s.index]
}

func (s rotation) step(now time.Time, stats device.PeerStats, onRelay bool) rotation {
	switch {
	case s.stopped:
		return s
	case onRelay || liveHandshake(stats, now):
		s.stopped = true
		return s
	case !s.retryAt.IsZero():
		if now.Before(s.retryAt) {
			return s
		}
		return s.assign(0, now)
	case now.Sub(s.since) < CandidateProbeTimeout:
		return s
	case s.index+1 < len(s.usable):
		return s.assign(s.index+1, now)
	default:
		return s.backOff(now)
	}
}

func (s rotation) assign(index int, now time.Time) rotation {
	s.index = index
	s.since = now
	s.retryAt = time.Time{}
	return s
}

func (s rotation) backOff(now time.Time) rotation {
	wait := candidateBackoff[min(s.failures, len(candidateBackoff)-1)]
	s.failures++
	s.retryAt = now.Add(wait)
	return s
}

func liveHandshake(stats device.PeerStats, now time.Time) bool {
	return !stats.LastHandshakeTime.IsZero() && now.Sub(stats.LastHandshakeTime) < HandshakeFreshness
}

// RunEndpointRotation calls step every RotationInterval until ctx is
// canceled. step is the device owner's rotate: read stats, Observe,
// re-apply on change. Errors are logged and the loop continues.
func RunEndpointRotation(ctx context.Context, step func(context.Context) error) {
	t := time.NewTicker(RotationInterval)
	defer t.Stop()
	runEndpointRotation(ctx, step, t.C)
}

func runEndpointRotation(ctx context.Context, step func(context.Context) error, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := step(ctx); err != nil {
				slog.Warn("endpoint rotation", "err", err)
			}
		}
	}
}
