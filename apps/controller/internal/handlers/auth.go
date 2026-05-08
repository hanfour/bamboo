// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AuthHandler implements bamboov1.AuthServiceServer.
type AuthHandler struct {
	bamboov1.UnimplementedAuthServiceServer

	tenants *repo.Tenants
	keys    *repo.PreAuthKeys
	pool    *db.Pool
}

// NewAuthHandler constructs an AuthHandler with required repositories.
func NewAuthHandler(pool *db.Pool) *AuthHandler {
	return &AuthHandler{
		tenants: repo.NewTenants(pool),
		keys:    repo.NewPreAuthKeys(pool),
		pool:    pool,
	}
}

// CreatePreAuthKey issues a new pre-auth key for the calling tenant.
//
// Phase 1 simplification: the caller's tenant is read from x-tenant-slug
// metadata (with the same default-tenant fallback as Coordinator.Register).
// Real authorization (admin role check) is tracked in Sprint 2 issue #11.
func (h *AuthHandler) CreatePreAuthKey(ctx context.Context, req *bamboov1.CreatePreAuthKeyRequest) (*bamboov1.CreatePreAuthKeyResponse, error) {
	slug := tenantSlugFromMetadata(ctx)
	tenant, err := h.tenants.GetOrCreate(ctx, slug, "Default Tenant", "100.64.0.0/24")
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

	return &bamboov1.CreatePreAuthKeyResponse{
		Key:    toProtoPreAuthKey(created),
		Secret: plaintext,
	}, nil
}

// RedeemPreAuthKey validates a presented secret and returns a session.
// The session payload is a placeholder until OIDC / JWT issuance lands.
func (h *AuthHandler) RedeemPreAuthKey(ctx context.Context, req *bamboov1.RedeemPreAuthKeyRequest) (*bamboov1.RedeemPreAuthKeyResponse, error) {
	if req.GetSecret() == "" {
		return nil, status.Error(codes.InvalidArgument, "secret is required")
	}
	key, err := h.redeemAndReturnKey(ctx, req.GetSecret())
	if err != nil {
		return nil, err
	}
	// TODO(#11): mint a real JWT once OIDC scaffold lands. For now we return
	// the secret echoed back so callers can integrate end-to-end.
	return &bamboov1.RedeemPreAuthKeyResponse{
		Session: &bamboov1.Session{
			AccessToken: "dev-" + key.ID.String(),
			TenantId:    key.TenantID.String(),
		},
	}, nil
}

// ListPreAuthKeys returns the calling tenant's keys. Pagination not yet
// implemented; returns up to 1000.
func (h *AuthHandler) ListPreAuthKeys(ctx context.Context, _ *bamboov1.ListPreAuthKeysRequest) (*bamboov1.ListPreAuthKeysResponse, error) {
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
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id: %v", err)
	}
	if err := h.keys.Revoke(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "revoke: %v", err)
	}
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
	return key, nil
}

// toProtoPreAuthKey converts a repo.PreAuthKey to its proto form.
// The plaintext secret is never included; only the hash-derived metadata.
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
