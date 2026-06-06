// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
)

// restHeartbeater posts each heartbeat to POST /api/v1/peers/heartbeat
// instead of speaking gRPC. The switch (vs. the previous gRPC stub
// the daemon used) is what lets the CLI participate in the §4 P2
// bandwidth metering side-channel — REST is the only transport that
// carries bytesSent / bytesReceived today; bumping the proto would
// regen a generated file that ships with every consumer SDK for the
// sake of two scalar fields.
//
// The session bearer is presented as Authorization: Bearer when set,
// matching the controller's prod-mode gate (peer-session credential
// required for peer-id-scoped endpoints). Slug is forwarded too so
// dev controllers without the gate keep working.
type restHeartbeater struct {
	httpClient *http.Client
	baseURL    string
	session    *peerSession
	tenantSlug string
}

func newRESTHeartbeater(session *peerSession, tenantSlug string) *restHeartbeater {
	return &restHeartbeater{
		httpClient: http.DefaultClient,
		baseURL:    controllerHTTPBase(),
		session:    session,
		tenantSlug: tenantSlug,
	}
}

// peerHeartbeatRequest mirrors the controller's
// apps/controller/internal/server/api_peers.go peerHeartbeatRequest
// shape. ConnectionPath stays empty in the CLI today; the dashboard
// gets path correlation from the Apple clients (which detect it
// from NETunnelProvider's path monitor) — a CLI follow-up can wire
// it from clients/cli/internal/sync/relay_fallback's notion of
// current path if a use case shows up.
type restHeartbeatRequest struct {
	PeerID              string   `json:"peerId"`
	KnownPolicyRevision int64    `json:"knownPolicyRevision"`
	Endpoints           []string `json:"endpoints,omitempty"`
	BytesSent           uint64   `json:"bytesSent,omitempty"`
	BytesReceived       uint64   `json:"bytesReceived,omitempty"`
	// NAT64EgressHealthy is the active egress's translator liveness, a
	// *bool side-channel (NAT64 Phase C3). omitempty drops it when nil so
	// a non-egress peer / pre-C3 controller sees nothing — the controller
	// reads absent as "judge by staleness only".
	NAT64EgressHealthy *bool `json:"nat64EgressHealthy,omitempty"`
}

type restHeartbeatResponse struct {
	PeersChanged          bool  `json:"peersChanged"`
	PolicyChanged         bool  `json:"policyChanged"`
	CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
}

// Heartbeat satisfies clientsync.Heartbeater. Errors propagate so the
// daemon can log+continue — restHeartbeater does NOT swallow the
// underlying HTTP error, leaving retry policy to RunHeartbeat (which
// today is "fire-and-forget on the next tick").
func (r *restHeartbeater) Heartbeat(ctx context.Context, args clientsync.HeartbeatArgs) (clientsync.HeartbeatResult, error) {
	body, err := json.Marshal(restHeartbeatRequest{
		PeerID:              args.PeerID,
		KnownPolicyRevision: args.KnownPolicyRevision,
		Endpoints:           args.Endpoints,
		BytesSent:           args.BytesSent,
		BytesReceived:       args.BytesReceived,
		NAT64EgressHealthy:  args.NAT64EgressHealthy,
	})
	if err != nil {
		return clientsync.HeartbeatResult{}, err
	}
	u, err := url.JoinPath(r.baseURL, "/api/v1/peers/heartbeat")
	if err != nil {
		return clientsync.HeartbeatResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return clientsync.HeartbeatResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := r.session.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if r.tenantSlug != "" {
		req.Header.Set("X-Tenant-Slug", r.tenantSlug)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return clientsync.HeartbeatResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return clientsync.HeartbeatResult{}, fmt.Errorf("heartbeat http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out restHeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return clientsync.HeartbeatResult{}, fmt.Errorf("decode heartbeat response: %w", err)
	}
	return clientsync.HeartbeatResult{
		PeersChanged:          out.PeersChanged,
		PolicyChanged:         out.PolicyChanged,
		CurrentPolicyRevision: out.CurrentPolicyRevision,
	}, nil
}
