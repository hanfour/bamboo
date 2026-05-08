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
type CoordinatorClient interface {
	WatchPeers(ctx context.Context, in *bamboov1.WatchPeersRequest) (WatchStream, error)
	Heartbeat(ctx context.Context, in *bamboov1.HeartbeatRequest) (*bamboov1.HeartbeatResponse, error)
}

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

func (a *grpcAdapter) Heartbeat(ctx context.Context, in *bamboov1.HeartbeatRequest) (*bamboov1.HeartbeatResponse, error) {
	return a.inner.Heartbeat(ctx, in)
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

// RunHeartbeat periodically pings the controller until ctx is canceled.
// Errors are logged and tolerated; the loop continues so a transient
// outage doesn't kill the daemon.
func RunHeartbeat(ctx context.Context, cli CoordinatorClient, peerID string) {
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := cli.Heartbeat(ctx, &bamboov1.HeartbeatRequest{PeerId: peerID}); err != nil {
				slog.Warn("heartbeat failed", "err", err)
			}
		}
	}
}

// RunWatchPeers opens a WatchPeers stream, applies every event to the
// cache, and re-applies the device configuration whenever the cache
// reports a change. Reconnects with exponential backoff if the stream
// breaks. Returns when ctx is canceled.
func RunWatchPeers(
	ctx context.Context,
	cli CoordinatorClient,
	dev Applier,
	priv wg.PrivateKey,
	cache *PeerCache,
	peerID string,
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
