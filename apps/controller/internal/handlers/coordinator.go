// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
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
	pool    *db.Pool
}

// NewCoordinatorHandler constructs the coordinator handler with the
// repositories it needs.
func NewCoordinatorHandler(pool *db.Pool) *CoordinatorHandler {
	return &CoordinatorHandler{
		tenants: repo.NewTenants(pool),
		users:   repo.NewUsers(pool),
		peers:   repo.NewPeers(pool),
		pool:    pool,
	}
}

// Register handles peer registration.
//
// Auth (Phase 1 simplification): tenant slug is read from gRPC metadata
// "x-tenant-slug" with a "default" fallback for local development. The
// tenant is auto-created on first use. PreAuthKey / OIDC enforcement is
// tracked in Sprint 2 issues #11 and #12.
func (h *CoordinatorHandler) Register(ctx context.Context, req *bamboov1.RegisterRequest) (*bamboov1.RegisterResponse, error) {
	if req.GetWireguardPublicKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "wireguard_public_key is required")
	}
	if req.GetHostname() == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	tenantSlug := tenantSlugFromMetadata(ctx)

	tenant, err := h.tenants.GetOrCreate(ctx, tenantSlug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tenant resolve: %v", err)
	}

	// Idempotency: same public key registers again -> return existing peer.
	existing, err := h.peers.FindByPubKey(ctx, tenant.ID, req.GetWireguardPublicKey())
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return nil, status.Errorf(codes.Internal, "peer lookup: %v", err)
	}

	var self *repo.Peer
	if existing != nil {
		self = existing
		slog.Info("re-registration", "peer_id", self.ID, "tenant", tenant.Slug)
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
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "peer insert: %v", err)
		}
		slog.Info("new peer registered", "peer_id", self.ID, "ip", self.IP, "tenant", tenant.Slug)
	}

	// Compose response.
	allPeers, err := h.peers.ListByTenant(ctx, tenant.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list peers: %v", err)
	}

	resp := &bamboov1.RegisterResponse{
		Self:           toProtoPeer(self),
		Peers:          make([]*bamboov1.Peer, 0, len(allPeers)),
		PolicyRevision: 1, // TODO: read from acl_policies once ACL handlers ship
		RelayServers:   []*bamboov1.RelayServer{},
	}
	for _, p := range allPeers {
		if p.ID == self.ID {
			continue
		}
		resp.Peers = append(resp.Peers, toProtoPeer(p))
	}
	return resp, nil
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
