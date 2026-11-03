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
// Order: wg.OrderCandidates puts same-LAN private candidates first,
// then public ones, then off-subnet private ones (still worth a probe
// over a VPN or other routed private network).
//
// Rotation: a peer with no live handshake CandidateProbeTimeout after
// a candidate was assigned moves to the next; after the last it waits
// candidateBackoff and restarts at the first.
//
// A peer stops rotating once either of these holds:
//   - it has a live handshake. If WireGuard roamed to an endpoint it
//     learned from the peer, that endpoint is kept: the rotator's
//     intent no longer changes, and the device leaves an endpoint
//     alone while the intent is unchanged (#309).
//   - the relay fallback moved it to a relay proxy. The relay owns the
//     endpoint from then on; rotation would only fight it. A relayed
//     peer never resumes.
//
// A stopped, non-relayed peer resumes when its last handshake is older
// than HandshakeFreshness and over one CandidateProbeTimeout window it
// sent bytes but received none: the path died under it. Resumption
// starts at the candidate after the current one, and the endpoint
// WireGuard had learned joins the end of the list. An idle peer (no
// bytes sent) stays put.
//
// Everything restarts at the best candidate when the controller sends
// a different candidate list, or when a change in this host's subnets
// reorders that peer's candidates (the host moved onto or off the
// peer's LAN). A restart can overwrite a learned endpoint, as any
// changed intent does.
//
// Rotation only changes where WireGuard sends; it sends nothing itself.
// A peer with no keepalive and no traffic cycles candidates without a
// handshake attempt on any of them, which costs only latency: the
// first packet goes to whichever candidate is current. BuildDeviceConfig
// sets a 25s keepalive on every peer, so in practice each candidate
// gets real attempts.
//
// With the default timings a peer with dead candidates is still probing
// (or backing off) when RunRelayFallback's 60s threshold fires, so the
// relay takes over and rotation stops for it.
type CandidateRotator struct {
	mu       stdsync.Mutex
	local    func() []netip.Prefix
	now      func() time.Time
	prefixes []netip.Prefix // last local subnets seen, normalized
	peers    map[string]rotation
}

// NewCandidateRotator returns a rotator that reads this host's subnets
// from local and the time from now. Both are injected for tests.
func NewCandidateRotator(local func() []netip.Prefix, now func() time.Time) *CandidateRotator {
	return &CandidateRotator{local: local, now: now, peers: make(map[string]rotation)}
}

// Select returns a copy of cfg with each peer's Endpoint set to its
// current candidate. A peer whose candidate list differs from the last
// call starts over at its best candidate; unchanged peers keep their
// rotation state, so calling Select again with the same config returns
// the same endpoints. Peers absent from cfg are forgotten.
func (r *CandidateRotator) Select(cfg *wg.DeviceConfig) *wg.DeviceConfig {
	if cfg == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.refreshLocal(now)
	out := *cfg
	out.Peers = slices.Clone(cfg.Peers)
	next := make(map[string]rotation, len(out.Peers))
	for i := range out.Peers {
		p := &out.Peers[i]
		key := p.PublicKey.Base64()
		st, ok := r.peers[key]
		if !ok || !slices.Equal(st.candidates, p.Candidates) {
			st = newRotation(p.Candidates, r.prefixes, now)
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
	changed := r.refreshLocal(now)
	for _, s := range stats {
		st, ok := r.peers[s.PublicKey]
		if !ok || len(st.list()) == 0 {
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

// refreshLocal re-reads this host's subnets. When they changed, every
// peer whose candidate order changed with them restarts. Reports
// whether any peer's current endpoint moved.
func (r *CandidateRotator) refreshLocal(now time.Time) bool {
	prefixes := normalizePrefixes(r.local())
	if slices.Equal(prefixes, r.prefixes) {
		return false
	}
	r.prefixes = prefixes
	changed := false
	for key, st := range r.peers {
		if slices.Equal(wg.OrderCandidates(st.candidates, prefixes), st.ordered) {
			continue
		}
		next := newRotation(st.candidates, prefixes, now)
		changed = changed || next.current() != st.current()
		r.peers[key] = next
	}
	return changed
}

func normalizePrefixes(in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		out = append(out, p.Masked())
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return slices.Compact(out)
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
