// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AuthHandler implements bamboov1.AuthServiceServer.
type AuthHandler struct {
	bamboov1.UnimplementedAuthServiceServer

	tenants *repo.Tenants
	users   *repo.Users
	keys    *repo.PreAuthKeys
	audits  *repo.AuditLogs
	pool    *db.Pool

	// OIDC + session config (set by NewAuthHandlerWithOIDC; nil for tests
	// that exercise only pre-auth-key paths).
	oidcBaseURL string
	sessionSec  []byte
	sessionTTL  time.Duration
}

// NewAuthHandler constructs an AuthHandler with required repositories.
// OIDC fields default to zero values; the pre-auth-key path works without
// them. Use SetOIDCConfig (or the NewAuthHandlerWithOIDC helper) to wire
// OIDC.
func NewAuthHandler(pool *db.Pool) *AuthHandler {
	return &AuthHandler{
		tenants:    repo.NewTenants(pool),
		users:      repo.NewUsers(pool),
		keys:       repo.NewPreAuthKeys(pool),
		audits:     repo.NewAuditLogs(pool),
		pool:       pool,
		sessionTTL: 24 * time.Hour,
	}
}

// SetOIDCConfig wires the OIDC base URL, session signing secret, and TTL.
// Callers should invoke this before serving traffic if OIDC is enabled.
func (h *AuthHandler) SetOIDCConfig(baseURL string, secret []byte, ttl time.Duration) {
	h.oidcBaseURL = baseURL
	h.sessionSec = secret
	if ttl > 0 {
		h.sessionTTL = ttl
	}
}

// StartOIDCFlow returns the URL the user should visit to begin login.
// We host /auth/{provider}/login, which mints a state token and redirects
// to the upstream provider.
func (h *AuthHandler) StartOIDCFlow(ctx context.Context, req *bamboov1.StartOIDCFlowRequest) (*bamboov1.StartOIDCFlowResponse, error) {
	if h.oidcBaseURL == "" {
		return nil, status.Error(codes.FailedPrecondition, "OIDC not configured")
	}
	provider := oidcProviderName(req.GetProvider())
	if provider == "" {
		return nil, status.Error(codes.InvalidArgument, "unsupported provider")
	}

	// Tenant slug is taken from x-tenant-slug metadata for now; a future
	// API might include it on the request itself.
	tenantSlug := tenantSlugFromMetadata(ctx)
	state, err := auth.IssueOIDCState(h.sessionSec, tenantSlug, "", "", 10*time.Minute)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue state: %v", err)
	}

	url := fmt.Sprintf("%s/auth/%s/login?tenant=%s&state=%s",
		h.oidcBaseURL, provider, tenantSlug, state)

	return &bamboov1.StartOIDCFlowResponse{
		AuthorizationUrl: url,
		State:            state,
	}, nil
}

// CompleteOIDCFlow handles the case where a non-browser client intercepts
// the provider redirect itself and submits the code over gRPC.
//
// The shared completion logic lives in the HTTP handler; for the gRPC
// path we keep this Unimplemented in Phase 1 to avoid duplicating the
// exchange code path. Future PRs can wire a shared helper.
func (h *AuthHandler) CompleteOIDCFlow(_ context.Context, _ *bamboov1.CompleteOIDCFlowRequest) (*bamboov1.CompleteOIDCFlowResponse, error) {
	return nil, status.Error(codes.Unimplemented,
		"use the HTTP /auth/{provider}/callback endpoint; gRPC path lands in a follow-up")
}

// CreatePreAuthKey issues a new pre-auth key for the calling tenant.
func (h *AuthHandler) CreatePreAuthKey(ctx context.Context, req *bamboov1.CreatePreAuthKeyRequest) (*bamboov1.CreatePreAuthKeyResponse, error) {
	if err := h.RequireAdmin(ctx, "preauthkey.create"); err != nil {
		return nil, err
	}
	slug := tenantSlugFromMetadata(ctx)
	tenant, err := h.tenants.GetOrCreate(ctx, slug, "Default Tenant", repo.DefaultTenantCIDR)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tenant resolve: %v", err)
	}

	id := uuid.New()
	plaintext, hash, err := auth.GenerateSecret(id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate secret: %v", err)
	}

	var expiresAt *time.Time
	if req.GetExpiresAt() != nil {
		t := req.GetExpiresAt().AsTime()
		expiresAt = &t
	}

	created, err := h.keys.Create(ctx, &repo.PreAuthKey{
		ID:          id,
		TenantID:    tenant.ID,
		Description: req.GetDescription(),
		SecretHash:  hash,
		Tags:        req.GetTags(),
		Reusable:    req.GetReusable(),
		Ephemeral:   req.GetEphemeral(),
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "insert key: %v", err)
	}

	auditLog(ctx, h.audits, &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       "preauthkey.create",
		ResourceType: "pre_auth_key",
		ResourceID:   &created.ID,
		Diff: marshalDiff(map[string]any{
			"description": created.Description,
			"tags":        created.Tags,
			"reusable":    created.Reusable,
			"ephemeral":   created.Ephemeral,
		}),
	})

	return &bamboov1.CreatePreAuthKeyResponse{
		Key:    toProtoPreAuthKey(created),
		Secret: plaintext,
	}, nil
}

// RedeemPreAuthKey validates a presented secret and returns a session.
func (h *AuthHandler) RedeemPreAuthKey(ctx context.Context, req *bamboov1.RedeemPreAuthKeyRequest) (*bamboov1.RedeemPreAuthKeyResponse, error) {
	if req.GetSecret() == "" {
		return nil, status.Error(codes.InvalidArgument, "secret is required")
	}
	key, err := h.redeemAndReturnKey(ctx, req.GetSecret())
	if err != nil {
		return nil, err
	}

	// Mint a real session JWT now that we have the auth pipeline.
	if h.sessionSec == nil {
		return &bamboov1.RedeemPreAuthKeyResponse{
			Session: &bamboov1.Session{
				AccessToken: "dev-" + key.ID.String(),
				TenantId:    key.TenantID.String(),
			},
		}, nil
	}

	// PreAuthKey redemption does not have a per-user identity; we mint a
	// session bound to the tenant only. UserID is the all-zero UUID to
	// signal "service / headless" caller.
	tok, err := auth.IssueSessionToken(h.sessionSec, auth.SessionClaims{
		TenantID: key.TenantID,
	}, h.sessionTTL)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue session: %v", err)
	}
	return &bamboov1.RedeemPreAuthKeyResponse{
		Session: &bamboov1.Session{
			AccessToken: tok,
			TenantId:    key.TenantID.String(),
			ExpiresAt:   timestamppb.New(time.Now().Add(h.sessionTTL)),
		},
	}, nil
}

// ListPreAuthKeys returns the calling tenant's keys.
func (h *AuthHandler) ListPreAuthKeys(ctx context.Context, _ *bamboov1.ListPreAuthKeysRequest) (*bamboov1.ListPreAuthKeysResponse, error) {
	if err := h.RequireAdmin(ctx, "preauthkey.list"); err != nil {
		return nil, err
	}
	slug := tenantSlugFromMetadata(ctx)
	tenant, err := h.tenants.GetBySlug(ctx, slug)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "tenant %q: %v", slug, err)
	}

	keys, err := h.keys.ListByTenant(ctx, tenant.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list keys: %v", err)
	}

	resp := &bamboov1.ListPreAuthKeysResponse{
		Keys: make([]*bamboov1.PreAuthKey, 0, len(keys)),
	}
	for _, k := range keys {
		resp.Keys = append(resp.Keys, toProtoPreAuthKey(k))
	}
	return resp, nil
}

// RevokePreAuthKey revokes the named key. Idempotent.
func (h *AuthHandler) RevokePreAuthKey(ctx context.Context, req *bamboov1.RevokePreAuthKeyRequest) (*bamboov1.RevokePreAuthKeyResponse, error) {
	if err := h.RequireAdmin(ctx, "preauthkey.revoke"); err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id: %v", err)
	}
	if err := h.keys.Revoke(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "revoke: %v", err)
	}

	auditLog(ctx, h.audits, &repo.AuditEvent{
		ActorType:    "system",
		Action:       "preauthkey.revoke",
		ResourceType: "pre_auth_key",
		ResourceID:   &id,
	})
	return &bamboov1.RevokePreAuthKeyResponse{}, nil
}

// redeemAndReturnKey is shared by RedeemPreAuthKey and Register's pre-auth
// path. It validates the presented secret and increments the use counter.
func (h *AuthHandler) redeemAndReturnKey(ctx context.Context, presentedSecret string) (*repo.PreAuthKey, error) {
	id, err := auth.ParseSecret(presentedSecret)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid pre-auth key format")
	}

	key, err := h.keys.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, status.Error(codes.Unauthenticated, "pre-auth key not found")
		}
		return nil, status.Errorf(codes.Internal, "lookup key: %v", err)
	}

	if key.RevokedAt != nil {
		return nil, status.Error(codes.PermissionDenied, "pre-auth key revoked")
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return nil, status.Error(codes.PermissionDenied, "pre-auth key expired")
	}
	if !key.Reusable && key.UseCount > 0 {
		return nil, status.Error(codes.PermissionDenied, "pre-auth key already used")
	}

	if err := auth.VerifyHash(presentedSecret, key.SecretHash); err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid pre-auth key")
	}

	if err := h.keys.MarkRedeemed(ctx, key.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "mark redeemed: %v", err)
	}

	auditLog(ctx, h.audits, &repo.AuditEvent{
		TenantID:     &key.TenantID,
		ActorType:    "system",
		Action:       "preauthkey.redeem",
		ResourceType: "pre_auth_key",
		ResourceID:   &key.ID,
	})
	return key, nil
}

// RequireAdmin gates a gRPC handler on the calling principal having
// admin role. Mirrors the REST requireAdmin behavior so the two
// protocols enforce the same policy:
//
//   - bearer JWT present + user.IsAdmin → allow
//   - bearer JWT present + user not admin → PermissionDenied
//   - bearer JWT present but invalid → Unauthenticated
//   - no bearer JWT → dev fallback; logs a warn-once-per-call and
//     allows. In production the gRPC interceptor (configured by
//     Auth.RequireAuth) blocks unauthenticated calls before they reach
//     the handler, so this branch only runs in dev mode.
//
// action is a short label included in the warn log + the permission-
// denied message; use the same string the REST handler audit-logs.
func (h *AuthHandler) RequireAdmin(ctx context.Context, action string) error {
	token := bearerFromMetadata(ctx)
	if token == "" {
		slog.Warn("gRPC admin path via dev-fallback (no JWT) — configure OIDC + an admin user in production",
			"action", action,
		)
		return nil
	}
	if h.sessionSec == nil {
		return status.Error(codes.Unauthenticated, "session signing not configured")
	}
	claims, err := auth.VerifySessionToken(h.sessionSec, token)
	if err != nil {
		return status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	user, err := h.users.GetByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return status.Error(codes.Unauthenticated, "user not found")
		}
		return status.Errorf(codes.Internal, "resolve user: %v", err)
	}
	if user.TenantID != claims.TenantID {
		return status.Error(codes.Unauthenticated, "tenant membership mismatch")
	}
	if !user.IsAdmin {
		return status.Errorf(codes.PermissionDenied, "admin role required for %s", action)
	}
	return nil
}

// bearerFromMetadata extracts a "Bearer <token>" value from the gRPC
// authorization metadata. Returns "" when no bearer is present.
func bearerFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, v := range md.Get("authorization") {
		if strings.HasPrefix(v, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
		}
	}
	return ""
}

// resolveBearerToken validates a session JWT and returns the bound tenant.
func (h *AuthHandler) resolveBearerToken(ctx context.Context, token string) (*repo.Tenant, error) {
	if h.sessionSec == nil {
		return nil, status.Error(codes.Unauthenticated, "session signing not configured")
	}
	claims, err := auth.VerifySessionToken(h.sessionSec, token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	// Defense in depth (audit H-1): a user-session JWT always carries a
	// non-nil subject (UserID). Reject any token that verified but has no
	// user — a relay / peer-session token shape must never stand in for a
	// user session on this Register bearer path, even if a future change
	// re-shared the signing construction. The relay-token HMAC domain is
	// the primary guard; this is the belt to that suspenders.
	if claims.UserID == uuid.Nil {
		return nil, status.Error(codes.Unauthenticated, "bearer token is not a user session")
	}
	t, err := h.tenants.GetByID(ctx, claims.TenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tenant by id: %v", err)
	}
	return t, nil
}

// oidcProviderName maps the proto enum to our internal slug.
func oidcProviderName(p bamboov1.OIDCProvider) string {
	switch p {
	case bamboov1.OIDCProvider_OIDC_PROVIDER_GOOGLE:
		return "google"
	case bamboov1.OIDCProvider_OIDC_PROVIDER_GITHUB:
		return "github"
	default:
		return ""
	}
}

// toProtoPreAuthKey converts a repo.PreAuthKey to its proto form.
func toProtoPreAuthKey(k *repo.PreAuthKey) *bamboov1.PreAuthKey {
	out := &bamboov1.PreAuthKey{
		Id:          k.ID.String(),
		TenantId:    k.TenantID.String(),
		Description: k.Description,
		Tags:        k.Tags,
		Reusable:    k.Reusable,
		Ephemeral:   k.Ephemeral,
		CreatedAt:   timestamppb.New(k.CreatedAt),
		UseCount:    k.UseCount,
	}
	if k.ExpiresAt != nil {
		out.ExpiresAt = timestamppb.New(*k.ExpiresAt)
	}
	if k.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*k.RevokedAt)
	}
	return out
}
