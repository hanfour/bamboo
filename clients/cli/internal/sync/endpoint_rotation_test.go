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

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func rotationFixture(local ...string) (*CandidateRotator, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	prefixes := make([]netip.Prefix, 0, len(local))
	for _, p := range local {
		prefixes = append(prefixes, netip.MustParsePrefix(p))
	}
	return NewCandidateRotator(func() []netip.Prefix { return prefixes }, clock.now), clock
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

func TestRotatorSelectFallsBackToFirstCandidate(t *testing.T) {
	r, _ := rotationFixture("172.16.0.0/16")
	if got := selected(t, r, rotationConfig(lanBad, remote)); got != lanBad {
		t.Fatalf("got %s, want controller first", got)
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
