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
	"os"
	"strings"
	"sync"
	"time"

	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
)

// peerSession holds the controller-issued peer-bound bearer token for
// the lifetime of the running CLI process. The token is minted on
// Register and on every re-register (idempotent on pubkey, so the
// controller refreshes it without allocating a new IP). The CLI
// presents it on:
//
//   - REST POST /api/v1/relay-token   — replaces the X-Tenant-Slug
//     header fallback. Once the controller's prod-mode gate (#1 / #3
//     follow-up) lands, this is the only credential channel that
//     keeps relay fallback working.
//   - gRPC Heartbeat / WatchPeers      — the Bearer is informational
//     today (the interceptor whitelists those methods), and becomes
//     load-bearing once the gate lands.
//
// Reads and writes are guarded with a RWMutex. The CLI is single-
// process so contention is minimal; the lock exists to make the
// happens-before relationship explicit when refresh() rotates the
// token mid-watch.
type peerSession struct {
	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

func (s *peerSession) set(token string, expiresAt time.Time) {
	s.mu.Lock()
	s.token = token
	s.expiresAt = expiresAt
	s.mu.Unlock()
}

// Token returns the current bearer or "" when no token has been
// minted yet (e.g. talking to an old controller without PR A).
func (s *peerSession) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// restRegisterResponse is the JSON shape of the controller's
// POST /api/v1/peers/register, including PR A's new
// peerSessionToken + peerSessionExpiresAt fields. We model only the
// fields the CLI uses; the wire format keeps additional ones, which
// json decoders ignore.
type restRegisterResponse struct {
	Self                 restPeer   `json:"self"`
	Peers                []restPeer `json:"peers"`
	PolicyRevision       int64      `json:"policyRevision"`
	PeerSessionToken     string     `json:"peerSessionToken,omitempty"`
	PeerSessionExpiresAt int64      `json:"peerSessionExpiresAt,omitempty"`
	// NAT64 (Phase C): the controller surfaces the tenant's DNS64 config and,
	// for an approved egress, the activation signal. reconcileEgress drives
	// the Tayga translator from Nat64EgressActive; DNS64 synthesis uses the
	// prefix. These had been absent from this struct, so a CLI egress (which
	// registers over REST, not gRPC) always saw active=false and never built
	// the translator. omitempty keeps the wire shape unchanged for non-egress.
	DNS64Enabled      bool   `json:"dns64Enabled,omitempty"`
	NAT64Prefix       string `json:"nat64Prefix,omitempty"`
	NAT64EgressActive bool   `json:"nat64EgressActive,omitempty"`
}

type restPeer struct {
	ID                 string   `json:"id"`
	TenantID           string   `json:"tenantId"`
	Hostname           string   `json:"hostname"`
	IP                 string   `json:"ip"`
	WireguardPublicKey string   `json:"wireguardPublicKey"`
	OS                 string   `json:"os,omitempty"`
	ClientVersion      string   `json:"clientVersion,omitempty"`
	Endpoints          []string `json:"endpoints,omitempty"`
	AllowedIps         []string `json:"allowedIps,omitempty"`
}

// restRegister calls POST /api/v1/peers/register and returns the
// response shaped as bamboov1.RegisterResponse so downstream code
// (wg.BuildDeviceConfig, cache.Replace, etc.) keeps using the proto
// getters without a refactor. Idempotent on wireguard_public_key on
// the controller, so refresh() can call this repeatedly to rotate
// the bearer without provisioning a new IP.
func restRegister(ctx context.Context, hostname, wgPublicKey, osName, version, preAuthKey, tenantSlug string, endpoints, advertisedRoutes []string, advertiseExitNode, advertiseNat64Egress bool) (*bamboov1.RegisterResponse, string, time.Time, error) {
	base := controllerHTTPBase()
	reqBody := map[string]any{
		"hostname":           hostname,
		"wireguardPublicKey": wgPublicKey,
		"os":                 osName,
		"clientVersion":      version,
		"preAuthKeySecret":   preAuthKey,
		"tenantSlug":         tenantSlug,
		"endpoints":          endpoints,
	}
	// Omit empty advertise fields so dev-fallback registers (no routes,
	// no exit-node) keep the wire shape identical to pre-#136/#137
	// deployments — keeps controller-side defaulting behaviour stable.
	if len(advertisedRoutes) > 0 {
		reqBody["advertisedRoutes"] = advertisedRoutes
	}
	if advertiseExitNode {
		reqBody["advertiseExitNode"] = true
	}
	if advertiseNat64Egress {
		reqBody["advertiseNat64Egress"] = true
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	u, err := url.JoinPath(base, "/api/v1/peers/register")
	if err != nil {
		return nil, "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, "", time.Time{}, fmt.Errorf("register http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var rj restRegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&rj); err != nil {
		return nil, "", time.Time{}, fmt.Errorf("decode register response: %w", err)
	}
	out := &bamboov1.RegisterResponse{
		Self:              restPeerToProto(rj.Self),
		Peers:             restPeersToProto(rj.Peers),
		PolicyRevision:    rj.PolicyRevision,
		Dns64Enabled:      rj.DNS64Enabled,
		Nat64Prefix:       rj.NAT64Prefix,
		Nat64EgressActive: rj.NAT64EgressActive,
	}
	var exp time.Time
	if rj.PeerSessionExpiresAt > 0 {
		exp = time.Unix(rj.PeerSessionExpiresAt, 0)
	}
	return out, rj.PeerSessionToken, exp, nil
}

func restPeerToProto(p restPeer) *bamboov1.Peer {
	if p.ID == "" {
		return nil
	}
	return &bamboov1.Peer{
		Id:                 p.ID,
		TenantId:           p.TenantID,
		Hostname:           p.Hostname,
		WireguardPublicKey: p.WireguardPublicKey,
		Ip:                 p.IP,
		Os:                 p.OS,
		ClientVersion:      p.ClientVersion,
		Endpoints:          p.Endpoints,
		AllowedIps:         p.AllowedIps,
	}
}

func restPeersToProto(in []restPeer) []*bamboov1.Peer {
	out := make([]*bamboov1.Peer, 0, len(in))
	for _, p := range in {
		out = append(out, restPeerToProto(p))
	}
	return out
}

// controllerHTTPBase returns the controller's HTTP base URL, the
// counterpart of flagAddr (which is gRPC). Defaults to localhost:8081
// in dev — matches the make local-up port wiring.
func controllerHTTPBase() string {
	base := os.Getenv("BAMBOO_CONTROLLER_HTTP_URL")
	if base == "" {
		base = "http://localhost:8081"
	}
	return base
}

// authedAdapter wraps a CoordinatorClient so the WatchPeers call
// context carries the current peer-session bearer as gRPC outgoing
// metadata. The controller's interceptor today whitelists
// WatchPeers (so the bearer is ignored), but the prod-mode gate
// follow-up will start enforcing it — this wiring ensures that
// landing the gate does not break in-flight CLIs.
//
// Heartbeat moved to REST (see restHeartbeater) so it doesn't go
// through this adapter anymore — that flow carries the bearer via
// the HTTP Authorization header directly.
type authedAdapter struct {
	inner   clientsync.CoordinatorClient
	session *peerSession
}

func newAuthedAdapter(inner clientsync.CoordinatorClient, session *peerSession) clientsync.CoordinatorClient {
	return &authedAdapter{inner: inner, session: session}
}

func (a *authedAdapter) WatchPeers(ctx context.Context, in *bamboov1.WatchPeersRequest) (clientsync.WatchStream, error) {
	return a.inner.WatchPeers(a.authCtx(ctx), in)
}

func (a *authedAdapter) authCtx(ctx context.Context) context.Context {
	tok := a.session.Token()
	if tok == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
}
