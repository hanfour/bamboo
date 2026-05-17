// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// peerSessionTokenTTL is how long a Register-issued peer session
// token is valid before the client must re-register (or, once renewal
// is wired, refresh via Heartbeat). 24h matches the OIDC session TTL
// and gives long-running clients a predictable rotation cadence
// without forcing churn on every heartbeat.
const peerSessionTokenTTL = 24 * time.Hour

// REST adapters for the Coordinator service. The Apple clients (and
// any future REST-only consumer) use these instead of speaking gRPC,
// since gRPC-Swift adds a substantial toolchain footprint that does
// not pay off for our small request shapes.
//
// All three endpoints delegate to the same CoordinatorHandler the
// gRPC server uses, so validation, IP allocation, audit logging and
// the events bus all stay on a single code path.

// peerRegisterRequest mirrors bamboov1.RegisterRequest in JSON form.
type peerRegisterRequest struct {
	Hostname           string   `json:"hostname"`
	WireguardPublicKey string   `json:"wireguardPublicKey"`
	OS                 string   `json:"os"`
	ClientVersion      string   `json:"clientVersion"`
	PreAuthKeySecret   string   `json:"preAuthKeySecret,omitempty"`
	TenantSlug         string   `json:"tenantSlug,omitempty"`
	Endpoints          []string `json:"endpoints,omitempty"`
}

// peerJSON is the shape used in the register response and the SSE
// peer_added / peer_updated events.
type peerJSON struct {
	ID                 string   `json:"id"`
	TenantID           string   `json:"tenantId"`
	Hostname           string   `json:"hostname"`
	IP                 string   `json:"ip"`
	WireguardPublicKey string   `json:"wireguardPublicKey"`
	OS                 string   `json:"os,omitempty"`
	ClientVersion      string   `json:"clientVersion,omitempty"`
	Endpoints          []string `json:"endpoints,omitempty"`
	// PeerDNSName is the short DNS-safe label (e.g. "mac-mini")
	// under which this peer is reachable in the tenant tailnet via
	// MagicDNS — resolves to "<peer_dns_name>.bamboo". Empty for
	// legacy peers that haven't re-registered since the column
	// landed; omitempty so the wire shape stays unchanged for
	// callers that don't care.
	PeerDNSName string `json:"peerDnsName,omitempty"`
	// AllowedIps is the L3 reachability set the controller computed
	// for this peer from the recipient's perspective. Empty when the
	// recipient is the one registering or when policy denies the
	// pair. omitempty so dev-fallback responses (no policy authored)
	// match the pre-enforcement wire format.
	AllowedIps []string `json:"allowedIps,omitempty"`
	// ApprovalStatus mirrors the proto Peer.approval_status (issue
	// #133). One of "pending" / "approved" / "rejected". On the
	// Register response: clients can read self.ApprovalStatus to
	// learn if they are queued. Empty in legacy responses pre-#133;
	// clients should treat empty as approved for back-compat.
	ApprovalStatus string `json:"approvalStatus,omitempty"`
}

type peerRegisterResponse struct {
	Self           peerJSON   `json:"self"`
	Peers          []peerJSON `json:"peers"`
	PolicyRevision int64      `json:"policyRevision"`
	// PeerSessionToken is a short-lived HMAC-signed token bound to the
	// peer (tenant_id + peer_id + wireguard_public_key) that the
	// caller presents as Authorization: Bearer on subsequent peer-
	// mutation endpoints (heartbeat, watch, relay-token mint). Optional
	// in v1: the controller still honors the legacy X-Tenant-Slug
	// fallback when this is absent, so existing clients keep working
	// during the rollout. Once all clients submit the token, a future
	// PR will tighten the require-auth gate to require it. Empty when
	// the controller fails to mint (logged server-side) so clients can
	// degrade rather than fail registration.
	PeerSessionToken     string `json:"peerSessionToken,omitempty"`
	PeerSessionExpiresAt int64  `json:"peerSessionExpiresAt,omitempty"`
}

func (h *HTTPServer) apiPeersRegister(w http.ResponseWriter, r *http.Request) {
	var body peerRegisterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}
	if body.Hostname == "" || body.WireguardPublicKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("hostname and wireguardPublicKey are required"))
		return
	}

	// Prod-mode gate. A register call must carry at least one of:
	//   - a pre-auth-key secret in the body
	//   - a peer-session bearer (re-register from a CLI that already
	//     holds a token; the coordinator handler re-validates against
	//     the peer row)
	//   - a user-session JWT (admin minting a peer through the API)
	// Pure X-Tenant-Slug fallback is rejected. The CoordinatorHandler's
	// resolveTenant applies the same gate when called over gRPC.
	if h.requireAuth {
		if body.PreAuthKeySecret == "" && h.peerSessionFromRequest(r) == nil {
			authn, err := h.authenticate(r)
			if err != nil || authn.claims == nil {
				writeError(w, http.StatusUnauthorized, errors.New("register requires a pre-auth key, peer-session bearer, or user-session credential"))
				return
			}
		}
	}

	// Forward x-tenant-slug from the explicit body field OR the
	// X-Tenant-Slug header into the gRPC handler's metadata channel —
	// the handler's resolveTenant reads from there. In prod mode the
	// resolveTenant gate ignores slug when no payload credential is
	// present, so this header is only consulted in dev.
	slug := body.TenantSlug
	if slug == "" {
		slug = r.Header.Get("X-Tenant-Slug")
	}
	ctx := r.Context()
	if slug != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-tenant-slug", slug)
		// AppendToOutgoingContext does not set incoming metadata, which
		// is what the handler reads. Wrap it explicitly.
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-tenant-slug", slug))
	}

	req := &bamboov1.RegisterRequest{
		Hostname:           body.Hostname,
		WireguardPublicKey: body.WireguardPublicKey,
		Os:                 body.OS,
		ClientVersion:      body.ClientVersion,
		Endpoints:          body.Endpoints,
	}
	if body.PreAuthKeySecret != "" {
		req.Credential = &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: body.PreAuthKeySecret,
		}
	} else if tok := bearerToken(r); tok != "" && h.peerSessionFromRequest(r) == nil {
		// User-session JWT (OIDC sign-in) is present and the bearer
		// isn't a peer-session token. Propagate it into the gRPC
		// RegisterRequest so coord.resolveCredential can resolve the
		// tenant from the JWT claims; otherwise the coordinator's
		// own require_auth gate rejects with "Register requires a
		// pre-auth key or bearer credential" even though the REST
		// gate above already authenticated the user. Peer-session
		// bearers (heartbeat / watch / relay-token) have a
		// different claim shape that resolveBearerToken would
		// reject — skip those so we don't trade a clean error
		// ("require auth") for a confusing one ("invalid bearer").
		req.Credential = &bamboov1.RegisterRequest_BearerToken{
			BearerToken: tok,
		}
	}
	resp, err := h.coord.Register(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}

	out := peerRegisterResponse{
		Self:           protoPeerToJSON(resp.GetSelf()),
		Peers:          protoPeersToJSON(resp.GetPeers()),
		PolicyRevision: resp.GetPolicyRevision(),
	}
	// Mint a peer session token for the caller. Failure here is
	// non-fatal — log and degrade rather than fail registration, since
	// the legacy slug-based path still works while clients roll out.
	if tok, exp, mintErr := h.mintPeerSession(resp.GetSelf(), body.WireguardPublicKey); mintErr != nil {
		slog.Warn("mint peer session token", "peer_id", resp.GetSelf().GetId(), "err", mintErr)
	} else {
		out.PeerSessionToken = tok
		out.PeerSessionExpiresAt = exp.Unix()
	}
	writeJSON(w, http.StatusOK, out)
}

// mintPeerSession issues a peer-bound bearer token derived from the
// Register response. Pulls tenant_id + peer_id from the response (the
// authoritative source after IP allocation) and the wireguard pubkey
// from the request (the client's identity, locked into the claim).
func (h *HTTPServer) mintPeerSession(self *bamboov1.Peer, wgPublicKey string) (string, time.Time, error) {
	if self == nil {
		return "", time.Time{}, errors.New("register response missing self")
	}
	tenantID, err := uuid.Parse(self.GetTenantId())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse tenant id: %w", err)
	}
	peerID, err := uuid.Parse(self.GetId())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse peer id: %w", err)
	}
	exp := time.Now().Add(peerSessionTokenTTL)
	tok, err := auth.IssuePeerSessionToken(h.secret, auth.PeerSessionClaims{
		TenantID:    tenantID,
		PeerID:      peerID,
		WGPublicKey: wgPublicKey,
		ExpiresAt:   exp.Unix(),
	}, peerSessionTokenTTL)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

type peerHeartbeatRequest struct {
	PeerID              string   `json:"peerId"`
	KnownPolicyRevision int64    `json:"knownPolicyRevision"`
	Endpoints           []string `json:"endpoints,omitempty"`
}

type peerHeartbeatResponse struct {
	PeersChanged          bool  `json:"peersChanged"`
	PolicyChanged         bool  `json:"policyChanged"`
	CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
}

func (h *HTTPServer) apiPeersHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body peerHeartbeatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}
	// Prod-mode gate. Heartbeat is peer-id-scoped, so the credential
	// must bind to *this* peer: either a peer-session bearer whose
	// claim matches body.PeerID, or a user-session JWT (admin
	// driving a peer manually). The previous slug-only path is
	// rejected.
	if h.requireAuth {
		if !h.peerCredentialAllows(r, body.PeerID) {
			writeError(w, http.StatusUnauthorized, errors.New("heartbeat requires a peer-session bearer matching the peerId, or a user-session credential"))
			return
		}
	}
	resp, err := h.coord.Heartbeat(r.Context(), &bamboov1.HeartbeatRequest{
		PeerId:              body.PeerID,
		KnownPolicyRevision: body.KnownPolicyRevision,
		Endpoints:           body.Endpoints,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, peerHeartbeatResponse{
		PeersChanged:          resp.GetPeersChanged(),
		PolicyChanged:         resp.GetPolicyChanged(),
		CurrentPolicyRevision: resp.GetCurrentPolicyRevision(),
	})
}

// apiPeersWatch streams WatchPeers events as Server-Sent Events. The
// peer_id is supplied via query string. Each event is encoded as:
//
//	event: <type>\n
//	data: <json>\n\n
//
// where <type> is one of peer_added, peer_updated, peer_removed,
// policy_changed. Browsers and Swift clients (URLSession bytes API)
// consume this directly without an SSE library.
func (h *HTTPServer) apiPeersWatch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	peerID := r.URL.Query().Get("peerId")
	// Prod-mode gate. Same logic as heartbeat — watch is peer-id-
	// scoped, so the credential must bind to this peer.
	if h.requireAuth {
		if !h.peerCredentialAllows(r, peerID) {
			writeError(w, http.StatusUnauthorized, errors.New("watch requires a peer-session bearer matching the peerId, or a user-session credential"))
			return
		}
	}
	ch, cancel, err := h.coord.SubscribePeer(r.Context(), peerID)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, event); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func writeSSE(w io.Writer, event *bamboov1.WatchPeersEvent) error {
	switch e := event.GetEvent().(type) {
	case *bamboov1.WatchPeersEvent_PeerAdded:
		return writeSSEEvent(w, "peer_added", map[string]any{
			"peer": protoPeerToJSON(e.PeerAdded.GetPeer()),
		})
	case *bamboov1.WatchPeersEvent_PeerUpdated:
		return writeSSEEvent(w, "peer_updated", map[string]any{
			"peer": protoPeerToJSON(e.PeerUpdated.GetPeer()),
		})
	case *bamboov1.WatchPeersEvent_PeerRemoved:
		return writeSSEEvent(w, "peer_removed", map[string]any{
			"peerId": e.PeerRemoved.GetPeerId(),
		})
	case *bamboov1.WatchPeersEvent_PolicyChanged:
		return writeSSEEvent(w, "policy_changed", map[string]any{
			"policyRevision": e.PolicyChanged.GetPolicyRevision(),
		})
	default:
		return nil
	}
}

func writeSSEEvent(w io.Writer, name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	return err
}

// writeGRPCError translates the grpc status of an error returned by
// the CoordinatorHandler into an HTTP status code. Errors without a
// gRPC status fall back to 500.
func writeGRPCError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	httpStatus := http.StatusInternalServerError
	switch st.Code().String() {
	case "InvalidArgument":
		httpStatus = http.StatusBadRequest
	case "Unauthenticated":
		httpStatus = http.StatusUnauthorized
	case "PermissionDenied":
		httpStatus = http.StatusForbidden
	case "NotFound":
		httpStatus = http.StatusNotFound
	case "AlreadyExists":
		httpStatus = http.StatusConflict
	case "ResourceExhausted":
		httpStatus = http.StatusTooManyRequests
	case "Unimplemented":
		httpStatus = http.StatusNotImplemented
	}
	writeError(w, httpStatus, errors.New(st.Message()))
}

func protoPeerToJSON(p *bamboov1.Peer) peerJSON {
	if p == nil {
		return peerJSON{}
	}
	return peerJSON{
		ID:                 p.GetId(),
		TenantID:           p.GetTenantId(),
		Hostname:           p.GetHostname(),
		IP:                 p.GetIp(),
		WireguardPublicKey: p.GetWireguardPublicKey(),
		OS:                 p.GetOs(),
		ClientVersion:      p.GetClientVersion(),
		Endpoints:          p.GetEndpoints(),
		PeerDNSName:        p.GetPeerDnsName(),
		AllowedIps:         p.GetAllowedIps(),
		ApprovalStatus:     p.GetApprovalStatus(),
	}
}

func protoPeersToJSON(in []*bamboov1.Peer) []peerJSON {
	out := make([]peerJSON, 0, len(in))
	for _, p := range in {
		out = append(out, protoPeerToJSON(p))
	}
	return out
}
