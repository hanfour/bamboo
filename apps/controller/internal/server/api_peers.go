// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

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
	Hostname           string `json:"hostname"`
	WireguardPublicKey string `json:"wireguardPublicKey"`
	OS                 string `json:"os"`
	ClientVersion      string `json:"clientVersion"`
	PreAuthKeySecret   string `json:"preAuthKeySecret,omitempty"`
	TenantSlug         string `json:"tenantSlug,omitempty"`
}

// peerJSON is the shape used in the register response and the SSE
// peer_added / peer_updated events.
type peerJSON struct {
	ID                 string `json:"id"`
	TenantID           string `json:"tenantId"`
	Hostname           string `json:"hostname"`
	IP                 string `json:"ip"`
	WireguardPublicKey string `json:"wireguardPublicKey"`
	OS                 string `json:"os,omitempty"`
	ClientVersion      string `json:"clientVersion,omitempty"`
}

type peerRegisterResponse struct {
	Self           peerJSON   `json:"self"`
	Peers          []peerJSON `json:"peers"`
	PolicyRevision int64      `json:"policyRevision"`
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

	// Forward x-tenant-slug from the explicit body field OR the
	// X-Tenant-Slug header into the gRPC handler's metadata channel —
	// the handler's resolveTenant reads from there.
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
	}
	if body.PreAuthKeySecret != "" {
		req.Credential = &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: body.PreAuthKeySecret,
		}
	}
	resp, err := h.coord.Register(ctx, req)
	if err != nil {
		writeGRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, peerRegisterResponse{
		Self:           protoPeerToJSON(resp.GetSelf()),
		Peers:          protoPeersToJSON(resp.GetPeers()),
		PolicyRevision: resp.GetPolicyRevision(),
	})
}

type peerHeartbeatRequest struct {
	PeerID              string `json:"peerId"`
	KnownPolicyRevision int64  `json:"knownPolicyRevision"`
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
	resp, err := h.coord.Heartbeat(r.Context(), &bamboov1.HeartbeatRequest{
		PeerId:              body.PeerID,
		KnownPolicyRevision: body.KnownPolicyRevision,
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
	}
}

func protoPeersToJSON(in []*bamboov1.Peer) []peerJSON {
	out := make([]peerJSON, 0, len(in))
	for _, p := range in {
		out = append(out, protoPeerToJSON(p))
	}
	return out
}
