// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/events"
	"github.com/hanfour/bamboo/apps/controller/internal/ipalloc"
	"github.com/hanfour/bamboo/apps/controller/internal/nat64"
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

// ApprovePeer flips a pending peer into approved and publishes a
// PeerAdded event so other peers' WatchPeers streams pick it up.
// Returns the post-update peer for caller convenience (HTTP layer
// wants to return the row in the response). Returns repo.ErrNotFound
// if the peer doesn't exist; returns nil + (changed=false) when the
// row was already in the approved state — the caller treats that as
// a successful idempotent no-op.
//
// Visibility contract (issue #133): only this method (and Register's
// auto-approve path) publishes PeerAdded for a peer that the rest of
// the tenant should now see. Without this, an admin clicking
// "approve" in the UI would update the DB but other peers' WG configs
// would lag until their next Register / Heartbeat poll round-tripped
// the change.
func (h *CoordinatorHandler) ApprovePeer(ctx context.Context, peerID uuid.UUID, adminUserID *uuid.UUID) (peer *repo.Peer, changed bool, err error) {
	rows, err := h.peers.Approve(ctx, peerID, adminUserID)
	if err != nil {
		return nil, false, err
	}
	updated, err := h.peers.GetByID(ctx, peerID)
	if err != nil {
		return nil, false, err
	}
	if rows == 0 {
		// Idempotent path — peer was already approved (or in a
		// terminal state). No event to publish.
		return updated, false, nil
	}
	h.bus.Publish(updated.TenantID, &bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerAdded{
			PeerAdded: &bamboov1.PeerAdded{Peer: toProtoPeer(updated)},
		},
	})
	return updated, true, nil
}

// RejectPeer flips a pending peer into rejected. No bus event fires —
// the peer was never visible to other peers and shouldn't be on
// reject. Returns repo.ErrNotFound if the peer doesn't exist; returns
// (updated, changed=false) when the row was already rejected.
func (h *CoordinatorHandler) RejectPeer(ctx context.Context, peerID uuid.UUID, adminUserID *uuid.UUID) (peer *repo.Peer, changed bool, err error) {
	rows, err := h.peers.Reject(ctx, peerID, adminUserID)
	if err != nil {
		return nil, false, err
	}
	updated, err := h.peers.GetByID(ctx, peerID)
	if err != nil {
		return nil, false, err
	}
	return updated, rows > 0, nil
}

// SetPeerStatus writes the data-plane liveness column (online /
// offline / disabled) and publishes a WatchPeers event so already-
// subscribed peers react without waiting for the next Register cycle
// (~30s heartbeat tick today). Mirrors the encapsulation pattern used
// by ApprovePeer.
//
// Event mapping:
//   - any → "disabled": PeerRemoved. Disabled is the only state that
//     removes a peer from other peers' WG configs (Register filter is
//     authoritative; see #161). Subscribers drop the peer immediately.
//   - "disabled" → online/offline: PeerAdded. The peer was invisible
//     to other peers while disabled; now it should be added back.
//   - online ↔ offline: PeerUpdated. Cosmetic for the UI's reachability
//     indicator; doesn't change anyone's WG config but the watch stream
//     is the canonical notification path so we publish for parity with
//     how heartbeat-driven last_seen ticks flow today.
//
// Idempotent: if old == new, no DB write, no event published. Returns
// the post-update peer row (or pre-update row when no change happened
// so the caller can still echo a stable response shape).
func (h *CoordinatorHandler) SetPeerStatus(ctx context.Context, peerID uuid.UUID, newStatus string) (peer *repo.Peer, changed bool, err error) {
	current, err := h.peers.GetByID(ctx, peerID)
	if err != nil {
		return nil, false, err
	}
	if current.Status == newStatus {
		return current, false, nil
	}
	if _, err := h.peers.SetStatus(ctx, peerID, newStatus); err != nil {
		return nil, false, err
	}
	updated, err := h.peers.GetByID(ctx, peerID)
	if err != nil {
		return nil, false, err
	}

	var event *bamboov1.WatchPeersEvent
	switch {
	case newStatus == "disabled":
		event = &bamboov1.WatchPeersEvent{
			Event: &bamboov1.WatchPeersEvent_PeerRemoved{
				PeerRemoved: &bamboov1.PeerRemoved{PeerId: peerID.String()},
			},
		}
	case current.Status == "disabled":
		// Only publish if the peer is also approved — a pending peer
		// being un-disabled stays invisible to other peers (Register's
		// approval filter excludes it). PeerAdded for a pending peer
		// would mislead subscribers into adding a peer the next
		// register would filter back out.
		if updated.ApprovalStatus == "approved" {
			event = &bamboov1.WatchPeersEvent{
				Event: &bamboov1.WatchPeersEvent_PeerAdded{
					PeerAdded: &bamboov1.PeerAdded{Peer: toProtoPeer(updated)},
				},
			}
		}
	default:
		// Cosmetic transition (online ↔ offline). Only publish for
		// approved peers — pending/rejected aren't visible regardless.
		if updated.ApprovalStatus == "approved" {
			event = &bamboov1.WatchPeersEvent{
				Event: &bamboov1.WatchPeersEvent_PeerUpdated{
					PeerUpdated: &bamboov1.PeerUpdated{Peer: toProtoPeer(updated)},
				},
			}
		}
	}
	if event != nil {
		h.bus.Publish(updated.TenantID, event)
	}
	return updated, true, nil
}

// PublishPeerUpdatedIfVisible publishes a PeerUpdated event on the
// WatchPeers stream IF the peer is currently visible to other peers
// (approved + not disabled). No-op for invisible peers — they aren't
// in any subscriber's WG config, and emitting PeerUpdated would
// mislead clients into adding a peer the next Register response
// would then filter out.
//
// Used by HTTP PATCH for hostname / dnsName / tags changes that
// don't transition status (status transitions have their own event
// via SetPeerStatus and shouldn't double-emit).
func (h *CoordinatorHandler) PublishPeerUpdatedIfVisible(tenantID uuid.UUID, peer *repo.Peer) {
	if peer.ApprovalStatus != "approved" {
		return
	}
	if peer.Status == "disabled" {
		return
	}
	h.bus.Publish(tenantID, &bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PeerUpdated{
			PeerUpdated: &bamboov1.PeerUpdated{Peer: toProtoPeer(peer)},
		},
	})
}

// PublishRelaysChanged broadcasts the fresh eligible-relay list to
// every active WatchPeers subscriber across every tenant (§4 P2
// multi-relay stage 4a). Relays are tenant-agnostic shared
// infrastructure (the relay_servers table has no tenant_id column),
// so PublishAll is the right routing primitive instead of looping
// per tenant.
//
// Called from the health-check reaper when a probe sweep flipped at
// least one relay's eligibility, and from the admin REST relay-create
// path so a brand-new relay reaches clients in seconds rather than
// at the next 30s health sweep.
func (h *CoordinatorHandler) PublishRelaysChanged(servers []*bamboov1.RelayServer) {
	h.bus.PublishAll(&bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_RelaysChanged{
			RelaysChanged: &bamboov1.RelaysChanged{RelayServers: servers},
		},
	})
}

// BumpPolicyRevision increments the tenant's policy_revision and
// publishes a PolicyChanged event so subscribed WatchPeers streams
// re-pull their per-peer allowed_ips. Used by admin actions that
// change the effective wire-layer view without touching ACL HCL —
// approving subnet routes (#136), flipping the exit-node bit (#137),
// and similar (closes #170).
//
// Both legs are needed:
//   - The DB bump is what makes Heartbeat see CurrentPolicyRevision !=
//     KnownPolicyRevision on the next tick, so peers that aren't
//     currently subscribed to WatchPeers (cold-reconnect window) still
//     pick up the change within one heartbeat interval.
//   - The bus publish is what lets currently-subscribed peers refresh
//     immediately without waiting for the heartbeat backstop.
//
// Bump failure is returned to the caller — the route/exit-node DB
// write already committed, so the safe behaviour is to surface the
// error and let the operator retry rather than silently leave peers
// stale.
func (h *CoordinatorHandler) BumpPolicyRevision(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	rev, err := h.policies.Bump(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	h.bus.Publish(tenantID, &bamboov1.WatchPeersEvent{
		Event: &bamboov1.WatchPeersEvent_PolicyChanged{
			PolicyChanged: &bamboov1.PolicyChanged{PolicyRevision: rev},
		},
	})
	return rev, nil
}

// ReconcileNAT64Egress is one sweep of one tenant for the C3 reaper: it
// marks any newly-stale approved egress unhealthy/'stale', recomputes the
// selected (lowest-UUID eligible) egress, and — if that selection differs
// from prevSelected — bumps the tenant policy revision so affected peers
// re-register and pick up the new <prefix>::/96 route. Returns the freshly
// selected egress, whether it bumped, and any error. On a bump error the
// caller should NOT advance its prevSelected (so the next sweep retries).
func (h *CoordinatorHandler) ReconcileNAT64Egress(ctx context.Context, tenantID, prevSelected uuid.UUID) (uuid.UUID, bool, error) {
	peers, err := h.peers.ListByTenant(ctx, tenantID)
	if err != nil {
		return uuid.Nil, false, err
	}
	now := time.Now()
	// Staleness leg: persist stale → unhealthy/'stale', and mutate the
	// in-memory peer so the recompute below sees it as ineligible without
	// a second DB read.
	for _, p := range peers {
		if !shouldMarkStale(p, now) {
			continue
		}
		if err := h.peers.SetNAT64EgressStale(ctx, p.ID); err != nil {
			slog.Warn("nat64 egress reaper: mark stale", "peer_id", p.ID, "err", err)
			continue
		}
		unhealthy := "unhealthy"
		p.NAT64EgressHealthStatus = &unhealthy
	}
	selected := selectEgress(peers, now)
	if selected == prevSelected {
		return selected, false, nil
	}
	if _, err := h.BumpPolicyRevision(ctx, tenantID); err != nil {
		return selected, false, err
	}
	return selected, true, nil
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

	tenant, ownerUserID, autoApprove, err := h.resolveCredential(ctx, req)
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
		// Backfill peer_dns_name for peers that registered before
		// MagicDNS landed. The column is nullable; we only fill it
		// when currently NULL (admins who set an explicit name later
		// must not be overwritten). Errors here are non-fatal — same
		// reasoning as the new-peer insert: we'd rather degrade than
		// fail re-registration.
		if self.PeerDNSName == nil {
			candidate, derr := ResolveDNSName(ctx, h.peers, tenant.ID, SlugifyHostname(self.Hostname))
			if derr != nil {
				slog.Warn("peer_dns_name backfill: resolve failed",
					"err", derr, "peer_id", self.ID, "tenant", tenant.Slug)
			} else if candidate != "" {
				if _, uerr := h.peers.UpdateDNSName(ctx, self.ID, &candidate); uerr != nil {
					slog.Warn("peer_dns_name backfill: update failed",
						"err", uerr, "peer_id", self.ID, "tenant", tenant.Slug)
				} else {
					self.PeerDNSName = &candidate
					slog.Info("peer_dns_name backfilled on re-register",
						"peer_id", self.ID, "dns_name", candidate)
				}
			}
		}
	} else {
		used, err := h.peers.UsedIPs(ctx, tenant.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list used IPs: %v", err)
		}
		ip, ip6, err := ipalloc.NextFreeDual(tenant.IPPool, tenant.IP6Pool, used)
		if err != nil {
			return nil, status.Errorf(codes.ResourceExhausted, "ip allocation: %v", err)
		}

		// MagicDNS auto-slug: derive a DNS-safe label from the reported
		// hostname and pick a non-colliding form within this tenant.
		// Failing this is non-fatal — we'd rather a peer register without
		// MagicDNS than reject onboarding — so on error we log and leave
		// peer_dns_name NULL; admin can fix via PATCH and the next
		// re-register will retry the auto-fill.
		dnsName, derr := ResolveDNSName(ctx, h.peers, tenant.ID, SlugifyHostname(req.GetHostname()))
		if derr != nil {
			slog.Warn("peer_dns_name auto-resolve failed; inserting without",
				"err", derr, "tenant", tenant.Slug, "hostname", req.GetHostname())
			dnsName = ""
		}
		var dnsPtr *string
		if dnsName != "" {
			dnsPtr = &dnsName
		}

		// Device-approval gating (issue #133). autoApprove came from
		// the credential path: pre-auth-key.auto_approve=true,
		// bearer-token admin register, or dev-fallback. Otherwise the
		// peer lands 'pending' and stays invisible to other peers
		// until an admin approves via the Web UI / REST API.
		initialApproval := "pending"
		if autoApprove {
			initialApproval = "approved"
		}
		self, err = h.peers.Insert(ctx, &repo.Peer{
			TenantID:           tenant.ID,
			UserID:             ownerUserID,
			Hostname:           req.GetHostname(),
			PeerDNSName:        dnsPtr,
			WireGuardPublicKey: req.GetWireguardPublicKey(),
			IP:                 ip,
			IP6:                ip6,
			OS:                 req.GetOs(),
			ClientVersion:      req.GetClientVersion(),
			Status:             "online",
			Endpoints:          req.GetEndpoints(),
			ApprovalStatus:     initialApproval,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "peer insert: %v", err)
		}
		slog.Info("new peer registered",
			"peer_id", self.ID,
			"ip", self.IP,
			"tenant", tenant.Slug,
			"dns_name", dnsName,
			"approval_status", self.ApprovalStatus,
		)
		isNewPeer = true
		auditDiff := map[string]any{"hostname": self.Hostname, "ip": self.IP, "os": self.OS, "approval_status": self.ApprovalStatus}
		if dnsName != "" {
			auditDiff["peer_dns_name"] = dnsName
		}
		auditLog(ctx, h.audits, &repo.AuditEvent{
			TenantID:     &tenant.ID,
			ActorType:    "system",
			Action:       "peer.register",
			ResourceType: "peer",
			ResourceID:   &self.ID,
			Diff:         marshalDiff(auditDiff),
		})
	}

	// Compose response.
	allPeers, err := h.peers.ListByTenant(ctx, tenant.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list peers: %v", err)
	}

	// ListEligible (not ListEnabled) filters out relays the
	// health-check reaper has flagged 'unhealthy' (§4 P2 multi-relay
	// registry, stage 1). Rows with status 'unknown' / NULL stay
	// included so a freshly inserted relay reaches clients before
	// its first probe lands.
	relays, err := h.relays.ListEligible(ctx)
	if err != nil {
		// Relay listing failure should not block registration —
		// peers can still come up via direct connection. Log and
		// continue with an empty relay list.
		slog.Warn("list relays for register response", "err", err)
	}

	policyDoc, currentRev := h.loadPolicyAndRevision(ctx, tenant.ID)

	resp := &bamboov1.RegisterResponse{
		Self:              toProtoPeer(self),
		Peers:             make([]*bamboov1.Peer, 0, len(allPeers)),
		PolicyRevision:    currentRev,
		RelayServers:      make([]*bamboov1.RelayServer, 0, len(relays)),
		Dns64Enabled:      tenant.DNS64Enabled,
		Nat64Prefix:       nat64.ResolvePrefix(tenant.NAT64Prefix),
		Nat64EgressActive: self.NAT64EgressApproved && tenant.DNS64Enabled,
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
	// Approval-status visibility filter (issue #133):
	//   - A pending or rejected peer must not appear in any other
	//     peer's view of the mesh. We skip them when assembling Peers.
	//   - A pending registering caller (self) likewise gets an empty
	//     Peers list — they cannot see other peers until the admin
	//     approves them. This is symmetric with the previous rule
	//     above and prevents a half-onboarded device from reading the
	//     tailnet's membership list. The empty list is fine for the
	//     client: it will heartbeat + retry Register on its normal
	//     cadence and pick up the populated list once approved.
	//
	// Liveness-status filter (mesh-debug 2026-05-23 side-finding):
	// disabled peers are out-of-band suspended (admin set status via
	// the API). Including them in another peer's Peers list would
	// load their pubkey + endpoints into WireGuard config; the client
	// would then spend cycles trying to handshake a peer the admin
	// has deactivated. Treat disabled like rejected/pending —
	// invisible to other peers and absent from the mesh until
	// re-enabled.
	selfApproved := self.ApprovalStatus == "approved"
	egressRoute := computeNAT64EgressRoute(tenant, allPeers, time.Now())
	for _, p := range allPeers {
		if p.ID == self.ID {
			continue
		}
		if !selfApproved {
			continue
		}
		if p.ApprovalStatus != "approved" {
			continue
		}
		if p.Status == "disabled" {
			continue
		}
		pp := toProtoPeer(p)
		pp.AllowedIps = allowedIPsFor(policyDoc, self, p, egressRoute)
		resp.Peers = append(resp.Peers, pp)
	}

	// Publish to other peers in the tenant after the response is built so
	// that the registering peer does not race against its own Register
	// response on a concurrent WatchPeers stream.
	//
	// Suppress the event for a peer that is currently pending — there is
	// nothing for other peers to see yet. The Approve handler publishes
	// the PeerAdded when the admin flips the gate. Rejected peers
	// (re-registering after rejection) likewise produce no event.
	if isNewPeer && selfApproved {
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
	if err := h.enforcePeerBinding(ctx, req.GetPeerId()); err != nil {
		return nil, err
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
			// Route through PublishPeerUpdatedIfVisible so a disabled
			// or pending peer's heartbeat-driven endpoint change
			// doesn't leak as a PeerUpdated to subscribers — they
			// already removed this peer from their WG config via the
			// SetPeerStatus PeerRemoved (or never added it for a
			// still-pending registration). Raw bus.Publish would
			// re-introduce the peer in their view of the mesh.
			h.PublishPeerUpdatedIfVisible(updated.TenantID, updated)
		}
	}

	_, currentRev := h.loadPolicyAndRevision(ctx, tenantID)
	return &bamboov1.HeartbeatResponse{
		PeersChanged:          endpointsChanged,
		PolicyChanged:         req.GetKnownPolicyRevision() != currentRev,
		CurrentPolicyRevision: currentRev,
	}, nil
}

// enforcePeerBinding rejects a request whose peer-session bearer is bound
// to a peer other than the one it targets (audit M-2). Without it, a
// valid peer-session token for peer A could Heartbeat / WatchPeers as
// peer B — endpoint poisoning, netmap snooping. Only the peer-session
// credential is bound; a user-session bearer (admin) or no bearer (dev /
// require_auth off) skips the check, matching the REST peerCredentialAllows
// behavior. The interceptor already validated the bearer's signature; this
// adds the identity binding the interceptor can't (it lacks the peer_id).
func (h *CoordinatorHandler) enforcePeerBinding(ctx context.Context, reqPeerID string) error {
	if h.auth == nil || len(h.auth.sessionSec) == 0 {
		return nil
	}
	token := bearerFromMetadata(ctx)
	if token == "" {
		return nil
	}
	claims, err := auth.VerifyPeerSessionToken(h.auth.sessionSec, token)
	if err != nil {
		// Not a peer-session token (user session, other shape). Peer
		// binding applies only to peer-session credentials.
		return nil
	}
	if claims.PeerID.String() != reqPeerID {
		return status.Error(codes.PermissionDenied, "peer-session token is not bound to the requested peer")
	}
	return nil
}

// WatchPeers streams peer-set and policy events to the calling peer for
// the lifetime of the stream.
func (h *CoordinatorHandler) WatchPeers(req *bamboov1.WatchPeersRequest, stream bamboov1.CoordinatorService_WatchPeersServer) error {
	ctx := stream.Context()
	if err := h.enforcePeerBinding(ctx, req.GetPeerId()); err != nil {
		return err
	}
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
//     attribute new peers to a human admin in the Users page. The
//     redeemed key is returned to the caller so Register can read
//     auto_approve and pick the new peer's approval_status (issue #133).
//  2. bearer_token credential -> resolved via the AuthHandler. owner
//     = nil today; will become the bearer's user when bearer tokens
//     learn user-scoped identity. autoApprove returns true because
//     a verified user-session token already establishes admin trust.
//  3. x-tenant-slug metadata fallback (dev convenience only — rejected
//     in prod mode where requireAuth=true). owner = nil and
//     autoApprove returns true because there's no admin context to
//     queue against; dev tooling expects immediate visibility.
//
// The prod-mode rejection is the gate the project-understanding doc
// Finding #1 calls for: a caller that knows a tenant slug should not
// be able to register a peer just because they know the slug. The
// caller must hold either a pre-auth-key (the canonical headless
// onboarding credential) or a bearer/session token issued by the
// controller. The REST adapter has its own corresponding check; this
// path covers gRPC Register.
func (h *CoordinatorHandler) resolveCredential(ctx context.Context, req *bamboov1.RegisterRequest) (tenant *repo.Tenant, ownerUserID *uuid.UUID, autoApprove bool, err error) {
	if secret := req.GetPreAuthKeySecret(); secret != "" {
		key, err := h.auth.redeemAndReturnKey(ctx, secret)
		if err != nil {
			return nil, nil, false, err
		}
		t, err := h.tenants.GetByID(ctx, key.TenantID)
		if err != nil {
			return nil, nil, false, status.Errorf(codes.Internal, "tenant by id: %v", err)
		}
		return t, key.CreatedBy, key.AutoApprove, nil
	}

	if token := req.GetBearerToken(); token != "" {
		t, err := h.auth.resolveBearerToken(ctx, token)
		// Bearer-token register implies an authenticated admin context —
		// auto-approve so a human signing into the Web UI to add their
		// own laptop doesn't have to click "approve" on their own
		// device.
		return t, nil, true, err
	}

	if h.requireAuth {
		return nil, nil, false, status.Error(codes.PermissionDenied, "Register requires a pre-auth key or bearer credential when require_auth is enabled")
	}

	slug := tenantSlugFromMetadata(ctx)
	t, terr := h.tenants.GetOrCreate(ctx, slug, "Default Tenant", repo.DefaultTenantCIDR)
	if terr != nil {
		return nil, nil, false, status.Errorf(codes.Internal, "tenant resolve: %v", terr)
	}
	// Dev-fallback path: no admin context, auto-approve so make
	// local-up + make local-bootstrap keep working without a manual
	// approval click in the middle.
	return t, nil, true, nil
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

// nat64EgressRoute carries the per-register NAT64 egress routing
// decision (NAT64 Phase C1): whether the tenant has DNS64 enabled (the
// master switch), the resolved /96 prefix, and the single active egress
// peer the <prefix>::/96 route points at. Zero value (enabled=false,
// egressID=uuid.Nil) emits no route.
type nat64EgressRoute struct {
	enabled  bool
	prefix   string // resolved "64:ff9b::/96" form
	egressID uuid.UUID
}

// nat64EgressStaleAfter is how long since a peer's last heartbeat before
// its egress is treated as unhealthy regardless of its last self-report
// — it catches a hard host crash that sends no final "down" heartbeat.
// 3× the client HeartbeatInterval (30s); duplicated here because the CLI
// interval lives in a separate module the controller cannot import.
const nat64EgressStaleAfter = 90 * time.Second

// isEgressEligible reports whether p may be the active NAT64 egress at
// `now`: approved + mesh-live + not confirmed-unhealthy + heartbeat
// fresh. health_status NULL/'unknown'/'healthy' are all eligible (only a
// confirmed 'unhealthy' is skipped); freshness is derived live from
// last_seen_at so the register path never routes to a translator that
// died inside PR 3's reaper gap (NAT64 Phase C3).
func isEgressEligible(p *repo.Peer, now time.Time) bool {
	if !p.NAT64EgressApproved || p.ApprovalStatus != "approved" || p.Status == "disabled" {
		return false
	}
	if p.NAT64EgressHealthStatus != nil && *p.NAT64EgressHealthStatus == "unhealthy" {
		return false
	}
	if p.LastSeenAt == nil || now.Sub(*p.LastSeenAt) > nat64EgressStaleAfter {
		return false
	}
	return true
}

// shouldMarkStale reports whether the reaper should write an approved
// egress's health to unhealthy/'stale' this sweep: it is a live-approved
// egress that stopped heartbeating past nat64EgressStaleAfter and is not
// already 'unhealthy' (don't clobber a self-reported 'translator down' or
// re-write 'stale'). A never-heartbeated egress (LastSeenAt nil) stays
// 'unknown' — it is ineligible anyway, and 'unknown' reads better than
// 'stale' for an egress that was never alive (NAT64 Phase C3).
func shouldMarkStale(p *repo.Peer, now time.Time) bool {
	if !p.NAT64EgressApproved || p.ApprovalStatus != "approved" || p.Status == "disabled" {
		return false
	}
	if p.NAT64EgressHealthStatus != nil && *p.NAT64EgressHealthStatus == "unhealthy" {
		return false
	}
	if p.LastSeenAt == nil {
		return false
	}
	return now.Sub(*p.LastSeenAt) > nat64EgressStaleAfter
}

// selectEgress returns the lowest-UUID eligible egress among peers, or
// uuid.Nil when none is eligible (NAT64 Phase C3). Shared by the register
// path (computeNAT64EgressRoute) and the reaper (ReconcileNAT64Egress) so
// they can never disagree on "who is the active egress".
func selectEgress(peers []*repo.Peer, now time.Time) uuid.UUID {
	var sel uuid.UUID
	for _, p := range peers {
		if !isEgressEligible(p, now) {
			continue
		}
		if sel == uuid.Nil || bytes.Compare(p.ID[:], sel[:]) < 0 {
			sel = p.ID
		}
	}
	return sel
}

// computeNAT64EgressRoute picks the tenant's single active NAT64 egress:
// the lowest-UUID eligible approved egress (health-aware, NAT64 Phase C3).
// Returns a zero egressID when no egress is eligible (no /96 route).
func computeNAT64EgressRoute(tenant *repo.Tenant, peers []*repo.Peer, now time.Time) nat64EgressRoute {
	return nat64EgressRoute{
		enabled:  tenant.DNS64Enabled,
		prefix:   nat64.ResolvePrefix(tenant.NAT64Prefix),
		egressID: selectEgress(peers, now),
	}
}

// allowedIPsFor returns the AllowedIps slice for dst as seen from src.
// nil when src is not permitted to reach dst — clients should skip the
// peer entirely.
//
// Contents (in order):
//
//  1. dst's tunnel /32 (IPv4) plus, when dst.IP6 is set, a /128
//     (IPv6) — the always-on baselines so src can reach dst's own
//     bamboo address on either family.
//
//  2. dst's approved subnet routes (issue #136). When the admin has
//     signed off on a subset of dst.AdvertisedRoutes, those CIDRs
//     become reachable through dst from every src the policy allows.
//
//  3. the tenant's NAT64 translation prefix (<prefix>::/96) when DNS64
//     is enabled and dst is the active approved egress (NAT64 Phase C1).
//     Routes src's synthesised v6 traffic to the translator peer.
//
//  4. 0.0.0.0/0 (and ::/0) when src has elected dst as its exit
//     node (issue #137) and dst is exit_node_approved. Default-route
//     reachability lets src forward public traffic through dst.
func allowedIPsFor(p *policy.Policy, src, dst *repo.Peer, egress nat64EgressRoute) []string {
	if !policy.Allow(p, peerView(src), peerView(dst)) {
		return nil
	}
	suffix := "/32"
	if addr, err := netip.ParseAddr(dst.IP); err == nil && addr.Is6() {
		suffix = "/128"
	}
	out := []string{dst.IP + suffix}
	// Dual-family (NAT64 Phase A §6): also advertise the peer's
	// deterministic IPv6 ULA /128 so one ACL rule reaches dst on both
	// families. dst.IP6 is always populated post-migration 00018; the
	// guard keeps legacy/empty rows (and the v6-in-IP defensive case
	// above) emitting a single entry.
	if dst.IP6 != "" {
		out = append(out, dst.IP6+"/128")
	}
	// Subnet router (issue #136): merge admin-approved CIDRs.
	out = append(out, dst.ApprovedRoutes...)
	// NAT64 egress (Phase C1): when the tenant has DNS64 enabled and dst
	// is the active approved egress, route the translation prefix to it
	// so a permitted src's synthesised v6 traffic reaches the translator.
	// Gated by policy.Allow above, exactly like the exit-node route.
	if egress.enabled && egress.egressID != uuid.Nil && dst.ID == egress.egressID {
		out = append(out, egress.prefix)
	}
	// Exit node (issue #137): default-route reachability when src
	// has dst pinned as its exit node AND admin has approved dst's
	// exit-node role. The double-gate is important: the requesting
	// peer's UsingExitNodePeerID alone shouldn't open 0.0.0.0/0
	// without admin approval, and admin approval alone shouldn't
	// force every other peer to add 0.0.0.0/0 unless they opt in.
	if dst.ExitNodeApproved && src.UsingExitNodePeerID != nil && *src.UsingExitNodePeerID == dst.ID {
		out = append(out, "0.0.0.0/0", "::/0")
	}
	return out
}

// toProtoPeer converts a repo.Peer into the proto type.
//
// Endpoints fallback: when the peer hasn't reported any STUN candidate
// (legacy manual-wg peers, or new clients whose first STUN probe is
// still in flight) but the controller has observed a WireGuard handshake
// from a NAT-mapped ip:port, surface that observed endpoint to other
// peers so they have a target to dial. Without this fallback, a fresh
// bamboo client trying to mesh with a legacy peer sees `endpoints=[]`
// and ends up with a WG TunnelConfiguration that has no peer endpoint
// at all — handshake never starts, mesh stays a hub-and-spoke fiction.
func toProtoPeer(p *repo.Peer) *bamboov1.Peer {
	endpoints := p.Endpoints
	if len(endpoints) == 0 && p.WGEndpoint != nil && *p.WGEndpoint != "" {
		endpoints = []string{*p.WGEndpoint}
	}
	out := &bamboov1.Peer{
		Id:                 p.ID.String(),
		TenantId:           p.TenantID.String(),
		Hostname:           p.Hostname,
		WireguardPublicKey: p.WireGuardPublicKey,
		Ip:                 p.IP,
		Ip6:                p.IP6,
		Os:                 p.OS,
		ClientVersion:      p.ClientVersion,
		Endpoints:          endpoints,
		CreatedAt:          timestamppb.New(p.CreatedAt),
		ApprovalStatus:     p.ApprovalStatus,
		Tags:               p.Tags,
	}
	if p.PeerDNSName != nil {
		out.PeerDnsName = *p.PeerDNSName
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
