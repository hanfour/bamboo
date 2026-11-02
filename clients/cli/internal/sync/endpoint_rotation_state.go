// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"net/netip"
	"slices"
	"time"

	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
)

// rotation is one peer's state in CandidateRotator. Methods take and
// return values; the rotator swaps the stored copy.
type rotation struct {
	candidates []string // controller list, compared to detect changes
	ordered    []string // candidates as wg.OrderCandidates ranked them
	learned    string   // endpoint WireGuard roamed to, appended on resume
	index      int      // into list()
	since      time.Time
	retryAt    time.Time // non-zero while backing off
	failures   int
	stopped    bool
	window     byteWindow // traffic sample while stopped
}

// byteWindow is a counter sample taken at the start of a staleness
// window. A zero at means no window is open.
type byteWindow struct {
	tx, rx uint64
	at     time.Time
}

func newRotation(candidates []string, local []netip.Prefix, now time.Time) rotation {
	return rotation{
		candidates: slices.Clone(candidates),
		ordered:    wg.OrderCandidates(candidates, local),
		since:      now,
	}
}

func (s rotation) list() []string {
	if s.learned == "" {
		return s.ordered
	}
	return append(slices.Clone(s.ordered), s.learned)
}

func (s rotation) current() string {
	l := s.list()
	if len(l) == 0 {
		return ""
	}
	return l[s.index%len(l)]
}

func (s rotation) step(now time.Time, stats device.PeerStats, onRelay bool) rotation {
	switch {
	case onRelay:
		s.stopped = true
		return s
	case liveHandshake(stats, now):
		s.stopped = true
		s.window = byteWindow{}
		return s
	case s.stopped:
		return s.watchStale(now, stats)
	case !s.retryAt.IsZero():
		if now.Before(s.retryAt) {
			return s
		}
		return s.assign(0, now)
	case now.Sub(s.since) < CandidateProbeTimeout:
		return s
	case s.index+1 < len(s.list()):
		return s.assign(s.index+1, now)
	default:
		return s.backOff(now)
	}
}

// watchStale resumes a stopped peer that, over one full window, sent
// bytes and received none. Anything else opens a fresh window.
func (s rotation) watchStale(now time.Time, stats device.PeerStats) rotation {
	w := s.window
	if !w.at.IsZero() && now.Sub(w.at) < CandidateProbeTimeout {
		return s
	}
	if !w.at.IsZero() && stats.TxBytes > w.tx && stats.RxBytes == w.rx {
		return s.resume(now, stats.Endpoint)
	}
	s.window = byteWindow{tx: stats.TxBytes, rx: stats.RxBytes, at: now}
	return s
}

func (s rotation) resume(now time.Time, learned string) rotation {
	s.stopped = false
	s.window = byteWindow{}
	s.failures = 0
	s.learned = ""
	if learned != "" && !slices.Contains(s.ordered, learned) {
		s.learned = learned
	}
	return s.assign((s.index+1)%len(s.list()), now)
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
