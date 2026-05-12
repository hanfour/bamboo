// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	"github.com/hanfour/bamboo/apps/controller/internal/ipalloc"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CoordinatorHandler implements bamboov1.CoordinatorServiceServer.
type CoordinatorHandler struct {
	bamboov1.UnimplementedCoordinatorServiceServer

	tenants  *repo.Tenants
	users    *repo.Users
	peers    *repo.Peers
	relays   *repo.Relays
	audits   *repo.AuditLogs
	policies *repo.Policies
	auth     *AuthHandler
	bus      *events.Bus
	pool     *db.Pool
	// requireAuth mirrors HTTPServer.requireAuth. When true, Register
	// rejects callers that present neither a pre-auth-key credential
	// nor a bearer-token credential — the x-tenant-slug metadata
	// fallback is no longer enough. Default false keeps dev / local
	// workflows working. Wired from cfg.Auth.RequireAuth via
	// SetRequireAuth.
	requireAuth bool
}

// SetRequireAuth flips the handler into prod-mode credential checking.
// Coordinator-specific because the REST adapter delegates here for
// peer onboarding; HTTPServer.SetRequireAuth applies the same gate to
// admin REST endpoints. Both knobs read from the same env var so a
// production deployment turns them on together.
func (h *CoordinatorHandler) SetRequireAuth(require bool) {
	h.requireAuth = require
}

// NewCoordinatorHandler constructs the coordinator handler. The auth
// handler is shared so we can re-use its pre-auth-key redemption logic on
// the Register path. The events bus is shared with WatchPeers stream
// subscribers.
func NewCoordinatorHandler(pool *db.Pool, auth *AuthHandler, bus *events.Bus) *CoordinatorHandler {
	return &CoordinatorHandler{
		tenants:  repo.NewTenants(pool),
		users:    repo.NewUsers(pool),
		peers:    repo.NewPeers(pool),
		relays:   repo.NewRelays(pool),
		audits:   repo.NewAuditLogs(pool),
		policies: repo.NewPolicies(pool),
		auth:     auth,
		bus:      bus,
		pool:     pool,
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

	tenant, ownerUserID, err := h.resolveCredential(ctx, req)
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
			UserID:             ownerUserID,
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

	policyDoc, currentRev := h.loadPolicyAndRevision(ctx, tenant.ID)

	resp := &bamboov1.RegisterResponse{
		Self:           toProtoPeer(self),
		Peers:          make([]*bamboov1.Peer, 0, len(allPeers)),
		PolicyRevision: currentRev,
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
		pp := toProtoPeer(p)
		pp.AllowedIps = allowedIPsFor(policyDoc, self, p)
		resp.Peers = append(resp.Peers, pp)
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

	tenantID, err := h.peers.UpdateLastSeen(ctx, peerID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "peer not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update last seen: %v", err)
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

	_, currentRev := h.loadPolicyAndRevision(ctx, tenantID)
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

// resolveCredential chooses the tenant + owning user for a Register
// call by precedence:
//
//  1. pre_auth_key_secret credential -> tenant from the key, owner =
//     the user who minted the key (pre_auth_keys.created_by). Lets us
//     attribute new peers to a human admin in the Users page.
//  2. bearer_token credential -> resolved via the AuthHandler. owner
//     = nil today; will become the bearer's user when bearer tokens
//     learn user-scoped identity.
//  3. x-tenant-slug metadata fallback (dev convenience only — rejected
//     in prod mode where requireAuth=true). owner = nil.
//
// The prod-mode rejection is the gate the project-understanding doc
// Finding #1 calls for: a caller that knows a tenant slug should not
// be able to register a peer just because they know the slug. The
// caller must hold either a pre-auth-key (the canonical headless
// onboarding credential) or a bearer/session token issued by the
// controller. The REST adapter has its own corresponding check; this
// path covers gRPC Register.
func (h *CoordinatorHandler) resolveCredential(ctx context.Context, req *bamboov1.RegisterRequest) (*repo.Tenant, *uuid.UUID, error) {
	if secret := req.GetPreAuthKeySecret(); secret != "" {
		key, err := h.auth.redeemAndReturnKey(ctx, secret)
		if err != nil {
			return nil, nil, err
		}
		t, err := h.tenants.GetByID(ctx, key.TenantID)
		if err != nil {
			return nil, nil, status.Errorf(codes.Internal, "tenant by id: %v", err)
		}
		return t, key.CreatedBy, nil
	}

	if token := req.GetBearerToken(); token != "" {
		t, err := h.auth.resolveBearerToken(ctx, token)
		return t, nil, err
	}

	if h.requireAuth {
		return nil, nil, status.Error(codes.PermissionDenied, "Register requires a pre-auth key or bearer credential when require_auth is enabled")
	}

	slug := tenantSlugFromMetadata(ctx)
	t, err := h.tenants.GetOrCreate(ctx, slug, "Default Tenant", "100.64.0.0/24")
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "tenant resolve: %v", err)
	}
	return t, nil, nil
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

// loadPolicyAndRevision fetches the tenant's current policy and
// revision. Returns (nil, 0) when no policy has been authored yet —
// callers interpret that as full-mesh fallback. A parse error on a
// previously-persisted policy is logged and treated as "no policy" so
// a malformed row cannot black-hole the whole tenant.
func (h *CoordinatorHandler) loadPolicyAndRevision(ctx context.Context, tenantID uuid.UUID) (*policy.Policy, int64) {
	rec, err := h.policies.Get(ctx, tenantID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, 0
	}
	if err != nil {
		slog.Warn("load policy for tenant", "tenant_id", tenantID, "err", err)
		return nil, 0
	}
	parsed, perr := policy.Parse("policy.hcl", rec.HCLSource)
	if perr != nil {
		slog.Warn("parse persisted policy", "tenant_id", tenantID, "revision", rec.Revision, "err", perr)
		return nil, rec.Revision
	}
	return parsed, rec.Revision
}

// peerView projects a repo.Peer onto the shape the L3 enforcer needs.
// User/Groups are left empty: only tag- and CIDR-based rules apply at
// the wire layer for now. user:/group: matchers require OIDC identity
// propagation through to the coordinator, which is not wired up yet.
func peerView(p *repo.Peer) policy.PeerView {
	view := policy.PeerView{Tags: p.Tags}
	if addr, err := netip.ParseAddr(p.IP); err == nil {
		view.IP = addr
	}
	return view
}

// allowedIPsFor returns the AllowedIps slice for dst as seen from src.
// nil when src is not permitted to reach dst — clients should skip the
// peer entirely. Today this is a single /32 (or /128) of the
// destination's tunnel IP; future revisions may add explicit CIDR
// routes (e.g., subnet routers).
func allowedIPsFor(p *policy.Policy, src, dst *repo.Peer) []string {
	if !policy.Allow(p, peerView(src), peerView(dst)) {
		return nil
	}
	suffix := "/32"
	if addr, err := netip.ParseAddr(dst.IP); err == nil && addr.Is6() {
		suffix = "/128"
	}
	return []string{dst.IP + suffix}
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
