// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	"github.com/hanfour/bamboo/apps/controller/internal/ipalloc"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CoordinatorHandler implements bamboov1.CoordinatorServiceServer.
type CoordinatorHandler struct {
	bamboov1.UnimplementedCoordinatorServiceServer

	tenants *repo.Tenants
	users   *repo.Users
	peers   *repo.Peers
	relays  *repo.Relays
	audits  *repo.AuditLogs
	auth    *AuthHandler
	bus     *events.Bus
	pool    *db.Pool
}

// NewCoordinatorHandler constructs the coordinator handler. The auth
// handler is shared so we can re-use its pre-auth-key redemption logic on
// the Register path. The events bus is shared with WatchPeers stream
// subscribers.
func NewCoordinatorHandler(pool *db.Pool, auth *AuthHandler, bus *events.Bus) *CoordinatorHandler {
	return &CoordinatorHandler{
		tenants: repo.NewTenants(pool),
		users:   repo.NewUsers(pool),
		peers:   repo.NewPeers(pool),
		relays:  repo.NewRelays(pool),
		audits:  repo.NewAuditLogs(pool),
		auth:    auth,
		bus:     bus,
		pool:    pool,
	}
}

// Register handles peer registration. See resolveTenant for credential
// precedence.
func (h *CoordinatorHandler) Register(ctx context.Context, req *bamboov1.RegisterRequest) (*bamboov1.RegisterResponse, error) {
	if req.GetWireguardPublicKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "wireguard_public_key is required")
	}
	if req.GetHostname() == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	tenant, err := h.resolveTenant(ctx, req)
	if err != nil {
		return nil, err
	}

	// Idempotency: same public key registers again -> return existing peer.
	existing, err := h.peers.FindByPubKey(ctx, tenant.ID, req.GetWireguardPublicKey())
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return nil, status.Errorf(codes.Internal, "peer lookup: %v", err)
	}

	var (
		self      *repo.Peer
		isNewPeer bool
	)
	if existing != nil {
		self = existing
		slog.Info("re-registration", "peer_id", self.ID, "tenant", tenant.Slug)
		// Honor endpoints reported on a re-register so a client that
		// re-registers (e.g. after STUN discovery completes between
		// boots) gets its endpoints persisted without waiting for the
		// next heartbeat.
		if eps := req.GetEndpoints(); len(eps) > 0 {
			changed, err := h.peers.UpdateEndpoints(ctx, self.ID, eps)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "update endpoints: %v", err)
			}
			if changed {
				self.Endpoints = eps
			}
		}
	} else {
		used, err := h.peers.UsedIPs(ctx, tenant.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list used IPs: %v", err)
		}
		ip, err := ipalloc.NextFree(tenant.IPPool, used)
		if err != nil {
			return nil, status.Errorf(codes.ResourceExhausted, "ip allocation: %v", err)
		}

		self, err = h.peers.Insert(ctx, &repo.Peer{
			TenantID:           tenant.ID,
			Hostname:           req.GetHostname(),
			WireGuardPublicKey: req.GetWireguardPublicKey(),
			IP:                 ip,
			OS:                 req.GetOs(),
			ClientVersion:      req.GetClientVersion(),
			Status:             "online",
			Endpoints:          req.GetEndpoints(),
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "peer insert: %v", err)
		}
		slog.Info("new peer registered", "peer_id", self.ID, "ip", self.IP, "tenant", tenant.Slug)
		isNewPeer = true
		auditLog(ctx, h.audits, &repo.AuditEvent{
			TenantID:     &tenant.ID,
			ActorType:    "system",
			Action:       "peer.register",
			ResourceType: "peer",
			ResourceID:   &self.ID,
			Diff:         marshalDiff(map[string]any{"hostname": self.Hostname, "ip": self.IP, "os": self.OS}),
		})
	}

	// Compose response.
	allPeers, err := h.peers.ListByTenant(ctx, tenant.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list peers: %v", err)
	}

	relays, err := h.relays.ListEnabled(ctx)
	if err != nil {
		// Relay listing failure should not block registration —
		// peers can still come up via direct connection. Log and
		// continue with an empty relay list.
		slog.Warn("list relays for register response", "err", err)
	}

	resp := &bamboov1.RegisterResponse{
		Self:           toProtoPeer(self),
		Peers:          make([]*bamboov1.Peer, 0, len(allPeers)),
		PolicyRevision: 1, // TODO: read from acl_policies once ACL handlers ship
		RelayServers:   make([]*bamboov1.RelayServer, 0, len(relays)),
	}
	for _, rs := range relays {
		resp.RelayServers = append(resp.RelayServers, &bamboov1.RelayServer{
			Id:        rs.ID.String(),
			Region:    rs.Region,
			Hostname:  rs.Hostname,
			Port:      int32(rs.Port),
			PublicKey: rs.PublicKey,
		})
	}
	for _, p := range allPeers {
		if p.ID == self.ID {
			continue
		}
		resp.Peers = append(resp.Peers, toProtoPeer(p))
	}

	// Publish to other peers in the tenant after the response is built so
	// that the registering peer does not race against its own Register
	// response on a concurrent WatchPeers stream.
	if isNewPeer {
		h.bus.Publish(tenant.ID, &bamboov1.WatchPeersEvent{
			Event: &bamboov1.WatchPeersEvent_PeerAdded{
				PeerAdded: &bamboov1.PeerAdded{Peer: resp.Self},
			},
		})
	}
	return resp, nil
}

// Heartbeat updates the peer's last_seen_at and reports whether the
// caller's known policy revision is stale.
func (h *CoordinatorHandler) Heartbeat(ctx context.Context, req *bamboov1.HeartbeatRequest) (*bamboov1.HeartbeatResponse, error) {
	if req.GetPeerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "peer_id is required")
	}
	peerID, err := uuid.Parse(req.GetPeerId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid peer_id: %v", err)
	}

	rows, err := h.peers.UpdateLastSeen(ctx, peerID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update last seen: %v", err)
	}
	if rows == 0 {
		return nil, status.Error(codes.NotFound, "peer not found")
	}

	// Apply endpoint updates if the client reported any. When the
	// endpoint list changes, publish a PeerUpdated to other peers in
	// the tenant so they can re-build their WireGuard config without
	// waiting for the next register.
	endpointsChanged := false
	if eps := req.GetEndpoints(); eps != nil {
		changed, err := h.peers.UpdateEndpoints(ctx, peerID, eps)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "update endpoints: %v", err)
		}
		endpointsChanged = changed
	}
	if endpointsChanged {
		updated, err := h.peers.GetByID(ctx, peerID)
		if err == nil && updated != nil {
			h.bus.Publish(updated.TenantID, &bamboov1.WatchPeersEvent{
				Event: &bamboov1.WatchPeersEvent_PeerUpdated{
					PeerUpdated: &bamboov1.PeerUpdated{Peer: toProtoPeer(updated)},
				},
			})
		}
	}

	// Phase 1: policy revision is hardcoded to 1 until ACL handlers ship.
	const currentRev = int64(1)
	return &bamboov1.HeartbeatResponse{
		PeersChanged:          endpointsChanged,
		PolicyChanged:         req.GetKnownPolicyRevision() != currentRev,
		CurrentPolicyRevision: currentRev,
	}, nil
}

// WatchPeers streams peer-set and policy events to the calling peer for
// the lifetime of the stream.
func (h *CoordinatorHandler) WatchPeers(req *bamboov1.WatchPeersRequest, stream bamboov1.CoordinatorService_WatchPeersServer) error {
	ctx := stream.Context()
	ch, cancel, err := h.SubscribePeer(ctx, req.GetPeerId())
	if err != nil {
		return err
	}
	defer cancel()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// SubscribePeer validates the peer ID and returns a channel of
// WatchPeersEvent values plus a cancel func. Used by both the gRPC
// streaming WatchPeers handler and the HTTP SSE adapter so they share
// the same validation and bus-subscription path.
func (h *CoordinatorHandler) SubscribePeer(ctx context.Context, peerIDStr string) (<-chan *bamboov1.WatchPeersEvent, func(), error) {
	if peerIDStr == "" {
		return nil, nil, status.Error(codes.InvalidArgument, "peer_id is required")
	}
	peerID, err := uuid.Parse(peerIDStr)
	if err != nil {
		return nil, nil, status.Errorf(codes.InvalidArgument, "invalid peer_id: %v", err)
	}
	peer, err := h.peers.GetByID(ctx, peerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil, status.Error(codes.NotFound, "peer not found")
		}
		return nil, nil, status.Errorf(codes.Internal, "get peer: %v", err)
	}
	ch, cancel := h.bus.Subscribe(peer.TenantID)
	return ch, cancel, nil
}

// resolveTenant chooses the tenant for a Register call by precedence:
//  1. pre_auth_key_secret credential -> tenant from the key
//  2. bearer_token credential -> Unimplemented (Sprint 2 #11)
//  3. x-tenant-slug metadata fallback (dev convenience)
func (h *CoordinatorHandler) resolveTenant(ctx context.Context, req *bamboov1.RegisterRequest) (*repo.Tenant, error) {
	if secret := req.GetPreAuthKeySecret(); secret != "" {
		key, err := h.auth.redeemAndReturnKey(ctx, secret)
		if err != nil {
			return nil, err
		}
		t, err := h.tenants.GetByID(ctx, key.TenantID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "tenant by id: %v", err)
		}
		return t, nil
	}

	if token := req.GetBearerToken(); token != "" {
		return h.auth.resolveBearerToken(ctx, token)
	}

	slug := tenantSlugFromMetadata(ctx)
	t, err := h.tenants.GetOrCreate(ctx, slug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tenant resolve: %v", err)
	}
	return t, nil
}

// tenantSlugFromMetadata extracts the tenant slug from gRPC metadata.
// Defaults to "default" for local development convenience.
func tenantSlugFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "default"
	}
	if vals := md.Get("x-tenant-slug"); len(vals) > 0 && vals[0] != "" {
		return vals[0]
	}
	return "default"
}

// toProtoPeer converts a repo.Peer into the proto type.
func toProtoPeer(p *repo.Peer) *bamboov1.Peer {
	out := &bamboov1.Peer{
		Id:                 p.ID.String(),
		TenantId:           p.TenantID.String(),
		Hostname:           p.Hostname,
		WireguardPublicKey: p.WireGuardPublicKey,
		Ip:                 p.IP,
		Os:                 p.OS,
		ClientVersion:      p.ClientVersion,
		Endpoints:          p.Endpoints,
		CreatedAt:          timestamppb.New(p.CreatedAt),
	}
	switch p.Status {
	case "online":
		out.Status = bamboov1.PeerStatus_PEER_STATUS_ONLINE
	case "offline":
		out.Status = bamboov1.PeerStatus_PEER_STATUS_OFFLINE
	case "disabled":
		out.Status = bamboov1.PeerStatus_PEER_STATUS_DISABLED
	default:
		out.Status = bamboov1.PeerStatus_PEER_STATUS_UNSPECIFIED
	}
	if p.LastSeenAt != nil {
		out.LastSeenAt = timestamppb.New(*p.LastSeenAt)
	}
	if p.UserID != nil {
		out.UserId = p.UserID.String()
	}
	return out
}
