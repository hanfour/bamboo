// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/hanfour/bamboo/clients/core/device"
	"github.com/hanfour/bamboo/clients/core/wg"
)

const (
	lanBad  = "192.168.1.108:51820"
	lanGood = "192.168.1.109:51820"
	public  = "220.135.1.2:51820"
	remote  = "10.9.9.9:51820"
)

// fakeClock also carries the fake local subnets so tests can move the
// host between networks.
type fakeClock struct {
	t     time.Time
	local []netip.Prefix
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func rotationFixture(local ...string) (*CandidateRotator, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0), local: mustPrefixes(local...)}
	return NewCandidateRotator(func() []netip.Prefix { return clock.local }, clock.now), clock
}

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(s))
	for _, p := range s {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

func rotationConfig(cands ...string) *wg.DeviceConfig {
	return &wg.DeviceConfig{Peers: []wg.PeerConfig{{
		PublicKey: wg.PublicKey{7}, Endpoint: cands[0], Candidates: cands,
	}}}
}

func peerKey() string { return wg.PublicKey{7}.Base64() }

func noHandshake(endpoint string) []device.PeerStats {
	return []device.PeerStats{{PublicKey: peerKey(), Endpoint: endpoint}}
}

func selected(t *testing.T, r *CandidateRotator, cfg *wg.DeviceConfig) string {
	t.Helper()
	return r.Select(cfg).Peers[0].Endpoint
}

func TestRotatorSelectFiltersRemoteLAN(t *testing.T) {
	r, _ := rotationFixture("203.0.113.0/24")
	cfg := rotationConfig(lanBad, public)
	if got := selected(t, r, cfg); got != public {
		t.Fatalf("vps picked %s, want public", got)
	}
	if cfg.Peers[0].Endpoint != lanBad {
		t.Fatal("Select mutated its input")
	}
}

func TestRotatorSelectKeepsSameLANFirst(t *testing.T) {
	r, _ := rotationFixture("192.168.1.0/24")
	if got := selected(t, r, rotationConfig(public, lanBad)); got != lanBad {
		t.Fatalf("same-LAN peer picked %s, want LAN", got)
	}
}

func TestRotatorSelectOffSubnetPrivateKeepsControllerOrder(t *testing.T) {
	r, _ := rotationFixture("172.16.0.0/16")
	if got := selected(t, r, rotationConfig(lanBad, remote)); got != lanBad {
		t.Fatalf("got %s, want controller first", got)
	}
}

func TestRotatorTriesOffSubnetPrivateAfterPublic(t *testing.T) {
	r, clock := rotationFixture("203.0.113.0/24")
	cfg := rotationConfig(lanBad, public)
	if got := selected(t, r, cfg); got != public {
		t.Fatalf("vps started on %s", got)
	}
	clock.advance(CandidateProbeTimeout)
	if !r.Observe(noHandshake(public), nil) || selected(t, r, cfg) != lanBad {
		t.Fatal("routed private candidate never tried")
	}
}

func TestRotatorResetsWhenLocalSubnetsChange(t *testing.T) {
	r, clock := rotationFixture("203.0.113.0/24")
	cfg := rotationConfig(lanBad, public)
	selected(t, r, cfg)
	clock.advance(CandidateProbeTimeout)
	r.Observe([]device.PeerStats{{PublicKey: peerKey(), Endpoint: public, LastHandshakeTime: clock.now()}}, nil)
	clock.local = mustPrefixes("192.168.1.0/24")
	if !r.Observe(noHandshake(public), nil) || selected(t, r, cfg) != lanBad {
		t.Fatal("moving onto the peer's LAN did not restart at the LAN candidate")
	}
	clock.advance(CandidateProbeTimeout)
	if !r.Observe(noHandshake(lanBad), nil) || selected(t, r, cfg) != public {
		t.Fatal("restarted peer stayed stopped")
	}
}

func TestRotatorIgnoresSubnetChangeThatKeepsOrder(t *testing.T) {
	r, clock := rotationFixture("203.0.113.0/24")
	cfg := rotationConfig(lanBad, public)
	selected(t, r, cfg)
	clock.advance(CandidateProbeTimeout)
	r.Observe(noHandshake(public), nil)
	clock.local = mustPrefixes("198.18.0.0/15")
	if got := selected(t, r, cfg); got != lanBad {
		t.Fatalf("unrelated subnet change reset rotation: %s", got)
	}
}

func stalledStats(endpoint string, handshake time.Time, tx, rx uint64) []device.PeerStats {
	return []device.PeerStats{{PublicKey: peerKey(), Endpoint: endpoint, LastHandshakeTime: handshake, TxBytes: tx, RxBytes: rx}}
}

func settled(t *testing.T, cands ...string) (*CandidateRotator, *fakeClock, *wg.DeviceConfig, time.Time) {
	t.Helper()
	r, clock := rotationFixture("192.168.1.0/24")
	cfg := rotationConfig(cands...)
	selected(t, r, cfg)
	hs := clock.now()
	r.Observe(stalledStats(learnedEP, hs, 100, 100), nil)
	clock.advance(HandshakeFreshness + time.Second)
	return r, clock, cfg, hs
}

const learnedEP = "198.51.100.40:41000"

func TestRotatorResumesWhenSendingIntoSilence(t *testing.T) {
	r, clock, cfg, hs := settled(t, lanBad, public)
	if r.Observe(stalledStats(learnedEP, hs, 200, 150), nil) {
		t.Fatal("resumed without a full window")
	}
	clock.advance(CandidateProbeTimeout)
	if !r.Observe(stalledStats(learnedEP, hs, 400, 150), nil) {
		t.Fatal("peer sending into silence did not resume rotation")
	}
	if got := selected(t, r, cfg); got != public {
		t.Fatalf("resume went to %s, want the candidate after the current intent", got)
	}
	clock.advance(CandidateProbeTimeout)
	if !r.Observe(stalledStats(public, hs, 500, 150), nil) || selected(t, r, cfg) != learnedEP {
		t.Fatal("learned endpoint not among rotated entries")
	}
}

func TestRotatorIdlePeerWithOldHandshakeStaysPut(t *testing.T) {
	r, clock, cfg, hs := settled(t, lanBad, public)
	for range 5 {
		if r.Observe(stalledStats(learnedEP, hs, 200, 150), nil) {
			t.Fatal("idle peer rotated")
		}
		clock.advance(CandidateProbeTimeout)
	}
	if got := selected(t, r, cfg); got != lanBad {
		t.Fatalf("idle peer intent moved to %s", got)
	}
}

func TestRotatorReceivingPeerStaysPut(t *testing.T) {
	r, clock, _, hs := settled(t, lanBad, public)
	r.Observe(stalledStats(learnedEP, hs, 200, 150), nil)
	clock.advance(CandidateProbeTimeout)
	if r.Observe(stalledStats(learnedEP, hs, 400, 160), nil) {
		t.Fatal("peer still receiving was rotated")
	}
}

func TestRotatorRelayedPeerNeverResumes(t *testing.T) {
	r, clock, _, hs := settled(t, lanBad, public)
	relayed := map[string]string{peerKey(): "127.0.0.1:40000"}
	r.Observe(stalledStats("127.0.0.1:40000", hs, 200, 150), relayed)
	clock.advance(CandidateProbeTimeout)
	if r.Observe(stalledStats("127.0.0.1:40000", hs, 400, 150), relayed) {
		t.Fatal("relayed peer resumed rotation")
	}
}

func TestRotatorSelectLeavesPeersWithoutCandidates(t *testing.T) {
	r, _ := rotationFixture()
	cfg := &wg.DeviceConfig{Peers: []wg.PeerConfig{{PublicKey: wg.PublicKey{7}}}}
	if got := selected(t, r, cfg); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRotatorAdvancesAfterProbeTimeout(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	cfg := rotationConfig(lanBad, lanGood, public)
	if got := selected(t, r, cfg); got != lanBad {
		t.Fatalf("start %s", got)
	}
	clock.advance(CandidateProbeTimeout - time.Second)
	if r.Observe(noHandshake(lanBad), nil) {
		t.Fatal("rotated before probe timeout")
	}
	clock.advance(time.Second)
	if !r.Observe(noHandshake(lanBad), nil) || selected(t, r, cfg) != lanGood {
		t.Fatal("did not rotate to second candidate")
	}
	clock.advance(CandidateProbeTimeout)
	if !r.Observe(noHandshake(lanGood), nil) || selected(t, r, cfg) != public {
		t.Fatal("did not rotate to third candidate")
	}
}

func TestRotatorBacksOffAfterExhaustingList(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	cfg := rotationConfig(lanBad, public)
	selected(t, r, cfg)
	clock.advance(CandidateProbeTimeout)
	r.Observe(noHandshake(lanBad), nil)
	for i, wait := range []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 120 * time.Second} {
		clock.advance(CandidateProbeTimeout)
		if r.Observe(noHandshake(public), nil) {
			t.Fatalf("cycle %d: exhausted list changed endpoint instead of backing off", i)
		}
		clock.advance(wait - time.Second)
		if r.Observe(noHandshake(public), nil) {
			t.Fatalf("cycle %d: retried before %s backoff", i, wait)
		}
		clock.advance(time.Second)
		if !r.Observe(noHandshake(public), nil) || selected(t, r, cfg) != lanBad {
			t.Fatalf("cycle %d: did not restart at first candidate after %s", i, wait)
		}
		clock.advance(CandidateProbeTimeout)
		if !r.Observe(noHandshake(lanBad), nil) || selected(t, r, cfg) != public {
			t.Fatalf("cycle %d: did not advance after restart", i)
		}
	}
}

func TestRotatorStopsOnLiveHandshake(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	cfg := rotationConfig(lanBad, public)
	selected(t, r, cfg)
	clock.advance(CandidateProbeTimeout)
	stats := []device.PeerStats{{PublicKey: peerKey(), Endpoint: lanBad, LastHandshakeTime: clock.now().Add(-time.Second)}}
	if r.Observe(stats, nil) {
		t.Fatal("rotated a peer with a live handshake")
	}
	clock.advance(10 * time.Minute)
	if r.Observe(noHandshake(lanBad), nil) || selected(t, r, cfg) != lanBad {
		t.Fatal("rotation resumed after settling")
	}
}

func TestRotatorNeverOverwritesLearnedEndpoint(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	cfg := rotationConfig(lanBad, public)
	selected(t, r, cfg)
	clock.advance(5 * CandidateProbeTimeout)
	learned := []device.PeerStats{{PublicKey: peerKey(), Endpoint: "198.51.100.40:41000", LastHandshakeTime: clock.now()}}
	if r.Observe(learned, nil) {
		t.Fatal("rotation overwrote learned endpoint")
	}
	if selected(t, r, cfg) != lanBad {
		t.Fatal("intent changed under a learned endpoint; device would overwrite it")
	}
}

func TestRotatorIgnoresStaleHandshake(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	cfg := rotationConfig(lanBad, public)
	selected(t, r, cfg)
	clock.advance(CandidateProbeTimeout)
	stale := []device.PeerStats{{PublicKey: peerKey(), Endpoint: lanBad, LastHandshakeTime: clock.now().Add(-HandshakeFreshness)}}
	if !r.Observe(stale, nil) {
		t.Fatal("stale handshake blocked rotation")
	}
}

func TestRotatorStopsForRelayedPeer(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	cfg := rotationConfig(lanBad, public)
	selected(t, r, cfg)
	clock.advance(CandidateProbeTimeout)
	relayed := map[string]string{peerKey(): "127.0.0.1:40000"}
	if r.Observe(noHandshake("127.0.0.1:40000"), relayed) {
		t.Fatal("rotated a relayed peer")
	}
	clock.advance(time.Hour)
	if r.Observe(noHandshake("127.0.0.1:40000"), nil) {
		t.Fatal("rotation resumed after relay took over")
	}
}

func TestRotatorResetsOnCandidateChange(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	selected(t, r, rotationConfig(lanBad, public))
	clock.advance(CandidateProbeTimeout)
	r.Observe([]device.PeerStats{{PublicKey: peerKey(), LastHandshakeTime: clock.now()}}, nil)
	next := rotationConfig(lanGood, lanBad, public)
	if got := selected(t, r, next); got != lanGood {
		t.Fatalf("reset picked %s", got)
	}
	clock.advance(CandidateProbeTimeout - time.Second)
	if r.Observe(noHandshake(lanGood), nil) {
		t.Fatal("probe timer not restarted on reset")
	}
	clock.advance(time.Second)
	if !r.Observe(noHandshake(lanGood), nil) || selected(t, r, next) != lanBad {
		t.Fatal("settled state survived candidate change")
	}
}

func TestRotatorUnchangedCandidatesKeepState(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	selected(t, r, rotationConfig(lanBad, public))
	clock.advance(CandidateProbeTimeout)
	r.Observe(noHandshake(lanBad), nil)
	if got := selected(t, r, rotationConfig(lanBad, public)); got != public {
		t.Fatalf("re-apply of same list reset rotation: %s", got)
	}
}

func TestRotatorForgetsRemovedPeers(t *testing.T) {
	r, clock := rotationFixture("192.168.1.0/24")
	selected(t, r, rotationConfig(lanBad, public))
	r.Select(&wg.DeviceConfig{})
	clock.advance(CandidateProbeTimeout)
	if r.Observe(noHandshake(lanBad), nil) {
		t.Fatal("removed peer still rotates")
	}
}

func TestRunEndpointRotationStepsOnTicks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	steps := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runEndpointRotation(ctx, func(context.Context) error {
			steps <- struct{}{}
			return errors.New("transient")
		}, ticks)
	}()
	ticks <- time.Now()
	ticks <- time.Now()
	<-steps
	<-steps
	cancel()
	<-done
}
