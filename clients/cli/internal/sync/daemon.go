// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

// Applier abstracts the device side so tests can drop in a fake. The
// production implementation is clients/core/device.Device.Apply.
type Applier interface {
	Apply(ctx context.Context, cfg *wg.DeviceConfig) error
}

// CoordinatorClient is the subset of bamboov1.CoordinatorServiceClient
// the daemon goroutines actually use. Tests inject a fake; production
// uses AdaptClient to wrap the generated gRPC stub.
//
// Heartbeat is intentionally NOT on this interface. It moved to the
// REST path (see Heartbeater) so the controller's bandwidth-sample
// side-channel (#187) fires for CLI clients without a proto bump.
type CoordinatorClient interface {
	WatchPeers(ctx context.Context, in *bamboov1.WatchPeersRequest) (WatchStream, error)
}

// HeartbeatArgs is the request the daemon hands to a Heartbeater on
// each tick. Decoupled from bamboov1.HeartbeatRequest so the REST
// transport (which carries fields the proto doesn't yet model —
// BytesSent / BytesReceived) and the gRPC transport can live behind
// the same interface.
type HeartbeatArgs struct {
	PeerID              string
	KnownPolicyRevision int64
	Endpoints           []string
	// BytesSent / BytesReceived are CUMULATIVE wg counters summed
	// across every peer on the local interface. Both zero ⇒ no
	// data; the controller skips its bandwidth-sample write.
	BytesSent     uint64
	BytesReceived uint64
	// NAT64EgressHealthy is this peer's NAT64 translator liveness when it
	// is the active egress; nil when it is not an egress (NAT64 Phase C3).
	// The controller reads it as a *bool side-channel.
	NAT64EgressHealthy *bool
}

// HeartbeatResult is the slim view of the response RunHeartbeat
// actually inspects. Future fields (e.g. "should restart relay")
// can extend it without breaking transports.
type HeartbeatResult struct {
	PeersChanged          bool
	PolicyChanged         bool
	CurrentPolicyRevision int64
}

// Heartbeater is the surface RunHeartbeat consumes. The CLI's
// production impl posts to REST /api/v1/peers/heartbeat carrying
// the peer-session bearer + cumulative byte counters. Tests inject
// a counter-based fake.
type Heartbeater interface {
	Heartbeat(ctx context.Context, args HeartbeatArgs) (HeartbeatResult, error)
}

// BytesReporter returns the current cumulative WireGuard byte
// counters this peer has observed (sum of every peer on the local
// interface). Called once per heartbeat tick; returning (0, 0)
// disables the bandwidth-sample write for that tick — appropriate
// when the reader can't read the device (wgctrl unsupported,
// transient permission failure). The reporter MUST be cheap to
// call; it runs on the heartbeat hot path.
type BytesReporter func() (bytesSent, bytesReceived uint64)

// NAT64HealthReporter returns this peer's NAT64 egress translator health
// for the heartbeat self-report: nil when the peer is not the active
// egress, else a pointer to the translator liveness (NAT64 Phase C3).
// Nil-safe: callers without an egress reconciler omit it.
type NAT64HealthReporter func() *bool

// WatchStream is the minimal surface we exercise from
// bamboov1.CoordinatorService_WatchPeersClient. The generated gRPC
// stream type satisfies it transparently.
type WatchStream interface {
	Recv() (*bamboov1.WatchPeersEvent, error)
}

// AdaptClient wraps the gRPC-generated client into the narrow
// interface this package consumes. Hides the variadic CallOption
// surface and the bigger stream interface from the daemon code path.
func AdaptClient(c bamboov1.CoordinatorServiceClient) CoordinatorClient {
	return &grpcAdapter{inner: c}
}

type grpcAdapter struct {
	inner bamboov1.CoordinatorServiceClient
}

func (a *grpcAdapter) WatchPeers(ctx context.Context, in *bamboov1.WatchPeersRequest) (WatchStream, error) {
	return a.inner.WatchPeers(ctx, in)
}

// HeartbeatInterval is the cadence the daemon pings the controller.
// Tunable via environment / flag in a future PR; 30s matches the
// server-side offline threshold.
const HeartbeatInterval = 30 * time.Second

// Backoff parameters for stream reconnect.
const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
)

// EndpointDiscoverer returns the locally-observed public endpoints
// for this peer. The CLI plugs in clients/core/stun.Discover here.
// Returns nil on failure (the daemon tolerates a missing endpoint).
type EndpointDiscoverer func() []string

// Refresher re-calls Coordinator.Register and returns the fresh
// response. Used by the watch loop to reconcile cache state after a
// PolicyChanged event — Register is idempotent for an existing pubkey
// and the response carries the controller's authoritative view of the
// peer set, including per-peer AllowedIps under the current policy.
//
// A nil Refresher disables policy-driven refresh; clients fall back to
// applying whatever AllowedIps were learned at the previous register.
type Refresher func(ctx context.Context) (*bamboov1.RegisterResponse, error)

// RelaysChangedHandler runs when the controller emits a
// RelaysChanged event on the WatchPeers stream (§4 P2 multi-relay
// stage 4b). The argument is the FRESH eligible-relay list — the
// caller in up.go re-runs pickRelay against it and logs which
// relay it would prefer next time around.
//
// Stage 4b ships the OBSERVE path: receive event, run picker, log
// the result. Actually swapping the live RelayClient mid-session
// (close + reopen + reset every peer's proxy port) is stage 4b-2
// — too much wiring change to bundle here.
//
// A nil handler disables the side-channel; the event is logged at
// info level and dropped.
type RelaysChangedHandler func(ctx context.Context, servers []*bamboov1.RelayServer)

// RunHeartbeat periodically pings the controller until ctx is canceled.
// When an endpoint discoverer is supplied it re-discovers on every
// tick so the controller learns about NAT-mapping changes between
// reboots / network swaps. When a bytes reporter is supplied (CLI
// production wiring), the cumulative wg counters travel along so the
// controller can fire its bandwidth-sample side-channel (#187). Both
// callbacks are nil-safe: callers running in degraded environments
// (no STUN, no wgctrl) just omit them. Errors are logged and tolerated;
// the loop continues so a transient outage doesn't kill the daemon.
func RunHeartbeat(ctx context.Context, hb Heartbeater, peerID string, discover EndpointDiscoverer, reportBytes BytesReporter, reportNAT64Health NAT64HealthReporter) {
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			args := HeartbeatArgs{PeerID: peerID}
			if discover != nil {
				args.Endpoints = discover()
			}
			if reportBytes != nil {
				args.BytesSent, args.BytesReceived = reportBytes()
			}
			if reportNAT64Health != nil {
				args.NAT64EgressHealthy = reportNAT64Health()
			}
			if _, err := hb.Heartbeat(ctx, args); err != nil {
				slog.Warn("heartbeat failed", "err", err)
			}
		}
	}
}

// RunWatchPeers opens a WatchPeers stream, applies every event to the
// cache, and re-applies the device configuration whenever the cache
// reports a change. PolicyChanged events trigger a re-register via
// refresh so the cache picks up freshly-computed AllowedIps; refresh
// may be nil, in which case PolicyChanged is logged but the cache is
// left stale until the next manual reconnect. Reconnects with
// exponential backoff if the stream breaks. Returns when ctx is
// canceled.
func RunWatchPeers(
	ctx context.Context,
	cli CoordinatorClient,
	dev Applier,
	priv wg.PrivateKey,
	cache *PeerCache,
	peerID string,
	refresh Refresher,
	onRelaysChanged RelaysChangedHandler,
	onRegister func(*bamboov1.RegisterResponse),
) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := cli.WatchPeers(ctx, &bamboov1.WatchPeersRequest{PeerId: peerID})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("watch open failed; will retry", "err", err, "backoff", backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = initialBackoff

		for {
			event, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				slog.Info("watch stream closed by server; reconnecting")
				break
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("watch stream broken; reconnecting", "err", err)
				break
			}
			if rc := event.GetRelaysChanged(); rc != nil {
				// §4 P2 multi-relay stage 4b. Controller broadcasts
				// the fresh eligible list whenever the set changes
				// (admin enable/disable, or the health reaper flips
				// a relay in/out of eligibility). We log the count
				// + hand the list to onRelaysChanged for re-picking;
				// the handler in up.go runs pickRelay against the
				// new list and logs which one it would prefer.
				// Mid-session swap is stage 4b-2.
				servers := rc.GetRelayServers()
				slog.Info("relays changed", "count", len(servers))
				if onRelaysChanged != nil {
					onRelaysChanged(ctx, servers)
				}
				continue
			}
			if pc := event.GetPolicyChanged(); pc != nil {
				slog.Info("policy changed", "revision", pc.GetPolicyRevision())
				if refresh == nil {
					continue
				}
				resp, err := refresh(ctx)
				if err != nil {
					slog.Warn("policy refresh failed", "err", err)
					continue
				}
				cache.Replace(resp.GetSelf(), resp.GetPeers(), resp.GetPolicyRevision())
				cfg, err := wg.BuildDeviceConfig(priv, resp)
				if err != nil {
					slog.Warn("build wireguard config after policy refresh", "err", err)
					continue
				}
				if err := dev.Apply(ctx, cfg); err != nil {
					slog.Warn("apply refreshed config to device", "err", err)
				}
				if onRegister != nil {
					onRegister(resp)
				}
				continue
			}
			if !cache.Apply(event) {
				continue
			}
			cfg, err := cache.BuildDeviceConfig(priv)
			if err != nil {
				slog.Warn("build wireguard config from updated cache", "err", err)
				continue
			}
			if err := dev.Apply(ctx, cfg); err != nil {
				slog.Warn("apply updated cache to device", "err", err)
			}
		}
	}
}

// sleepCtx waits for d or ctx cancel, whichever comes first. Returns
// false when the wait was interrupted by cancellation.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(prev time.Duration) time.Duration {
	next := prev * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}
