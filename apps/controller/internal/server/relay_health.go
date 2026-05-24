// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// relayHealthInterval is how often the reaper sweeps every enabled
// relay. 30s is fast enough that a relay flap surfaces to clients
// within one heartbeat cycle (HeartbeatInterval=30s on the client),
// and slow enough that the reaper goroutine + per-relay HTTP probe
// cost stays trivial even at hundreds of relays.
var relayHealthInterval = 30 * time.Second

// relayHealthProbeTimeout caps one HTTP /healthz probe. A relay
// that doesn't answer in 3s is unreachable as far as a peer is
// concerned (default WireGuard handshake retry is 5s), so the
// timeout is tighter than that to give the reaper headroom for
// retries before client traffic notices.
var relayHealthProbeTimeout = 3 * time.Second

// StartRelayHealthReaper launches the §4 P2 multi-relay registry's
// background health-check goroutine. The reaper iterates the
// currently-enabled relay set, probes each /healthz, and persists
// the result so the Coordinator's ListEligible filter can drop
// relays that have flipped unreachable.
//
// The goroutine exits when ctx is canceled. Exposed separately from
// Run so e2e tests can call it on demand (and so the reaper test
// can drive a single sweep without spawning the whole server).
//
// Probes use a dedicated http.Client (NOT http.DefaultClient) so a
// misconfigured relay with a bad cert can't poison every other
// caller in the controller process by mutating the default
// transport.
func (h *HTTPServer) StartRelayHealthReaper(ctx context.Context) {
	if h == nil || h.relays == nil {
		return
	}
	client := newRelayHealthClient()
	go func() {
		// One immediate sweep on startup so a controller restart
		// after a long outage reflects current relay health within
		// seconds rather than waiting the first tick. Subsequent
		// sweeps follow the interval.
		h.runRelayHealthSweep(ctx, client)
		t := time.NewTicker(relayHealthInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.runRelayHealthSweep(ctx, client)
			}
		}
	}()
}

// runRelayHealthSweep probes every enabled relay in parallel and
// records the result. Parallel because the per-probe deadline is
// 3s — sequential would stretch a 10-relay sweep to 30s, exceeding
// the 30s sweep interval and starving subsequent sweeps.
//
// We DON'T probe deleted_at IS NOT NULL rows — those are draining
// and the existing sessions ride out on the relay process itself
// rather than via the controller's eligibility list.
func (h *HTTPServer) runRelayHealthSweep(ctx context.Context, client *http.Client) {
	relays, err := h.relays.ListEnabled(ctx)
	if err != nil {
		slog.Warn("relay health: list enabled", "err", err)
		return
	}
	if len(relays) == 0 {
		return
	}
	var wg sync.WaitGroup
	wg.Add(len(relays))
	for _, rs := range relays {
		go func(id uuid.UUID, hostname string, port int) {
			defer wg.Done()
			status, probeErr := probeRelayHealth(ctx, client, hostname, port)
			if err := h.relays.UpdateHealth(ctx, id, status, probeErr); err != nil {
				slog.Warn("relay health: UpdateHealth",
					"relay_id", id, "hostname", hostname, "err", err)
			}
		}(rs.ID, rs.Hostname, rs.Port)
	}
	wg.Wait()
}

// probeRelayHealth returns the status string + short error reason
// to persist for one relay. Pulled out so a unit test can drive it
// against an httptest server without spinning up the reaper loop.
//
// Returns "healthy" / "unhealthy". Error reason is empty on
// healthy; on unhealthy it's the short cause text (e.g. "timeout",
// "status 503"). Body bytes are NOT included to avoid storing
// secrets the relay might accidentally surface.
func probeRelayHealth(ctx context.Context, client *http.Client, hostname string, port int) (status, probeErr string) {
	url := fmt.Sprintf("https://%s:%d/healthz", hostname, port)
	probeCtx, cancel := context.WithTimeout(ctx, relayHealthProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return "unhealthy", err.Error()
	}
	resp, err := client.Do(req)
	if err != nil {
		// Trim the URL out of the error string — it's already in
		// the relay row, repeating it here is noise.
		return "unhealthy", trimErrForUI(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "unhealthy", fmt.Sprintf("status %d", resp.StatusCode)
	}
	return "healthy", ""
}

// trimErrForUI strips the URL/host fragment that http.Client errors
// embed by default, leaving just the short cause (e.g. "timeout",
// "connection refused"). Keeps the LastHealthError column compact
// for the admin UI table.
func trimErrForUI(err error) string {
	s := err.Error()
	// Common patterns: 'Get "https://...": context deadline exceeded'.
	// Find the last ': ' and keep the tail.
	if i := lastIndexOfColonSpace(s); i >= 0 && i+2 < len(s) {
		return s[i+2:]
	}
	return s
}

func lastIndexOfColonSpace(s string) int {
	last := -1
	for i := 0; i+1 < len(s); i++ {
		if s[i] == ':' && s[i+1] == ' ' {
			last = i
		}
	}
	return last
}

// newRelayHealthClient returns the dedicated http.Client the reaper
// uses for probes. Customised vs http.DefaultClient on two axes:
//   - per-request timeout is enforced via context, so the client
//     timeout is the wall ceiling for the WHOLE roundtrip including
//     TLS handshake; a slow relay can't park the goroutine.
//   - TLS config defaults are explicit so an operator's broken
//     /etc/ssl trust store doesn't silently treat self-signed
//     relays as healthy (probe will fail with cert error, which
//     surfaces in last_health_error).
func newRelayHealthClient() *http.Client {
	return &http.Client{
		Timeout: relayHealthProbeTimeout + time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			// No connection reuse across probes — a relay that goes
			// half-up (TCP accepts, TLS handshake hangs) leaves a
			// stuck conn the next probe would re-pick. Cheaper to
			// pay the handshake than chase HOL blocking bugs at
			// the once-per-30s cadence.
			DisableKeepAlives: true,
		},
	}
}
