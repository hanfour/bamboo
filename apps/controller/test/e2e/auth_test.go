// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPreAuthKey_CreateAndRedeemViaRegister(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	created, err := f.auth.CreatePreAuthKey(ctx, &bamboov1.CreatePreAuthKeyRequest{
		Description: "e2e-test-key",
		Reusable:    false,
	})
	if err != nil {
		t.Fatalf("CreatePreAuthKey: %v", err)
	}
	if created.GetSecret() == "" {
		t.Fatal("CreatePreAuthKey returned empty secret")
	}
	if created.GetKey().GetId() == "" {
		t.Fatal("CreatePreAuthKey returned empty key id")
	}

	// Use the secret on Register. No tenant metadata — the secret should
	// resolve the tenant on its own.
	plainCtx := context.Background()
	resp, err := f.coord.Register(plainCtx, &bamboov1.RegisterRequest{
		Credential: &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: created.GetSecret(),
		},
		Hostname:           "via-key",
		WireguardPublicKey: randomPubKey(t),
		Os:                 "linux",
	})
	if err != nil {
		t.Fatalf("Register with key: %v", err)
	}
	if resp.GetSelf().GetTenantId() != created.GetKey().GetTenantId() {
		t.Errorf("registered tenant %s, want %s",
			resp.GetSelf().GetTenantId(), created.GetKey().GetTenantId())
	}
}

func TestPreAuthKey_NotReusableRejectsSecondUse(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	created, err := f.auth.CreatePreAuthKey(ctx, &bamboov1.CreatePreAuthKeyRequest{
		Description: "single-use",
		Reusable:    false,
	})
	if err != nil {
		t.Fatalf("CreatePreAuthKey: %v", err)
	}

	// First redemption succeeds.
	if _, err := f.coord.Register(context.Background(), &bamboov1.RegisterRequest{
		Credential: &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: created.GetSecret(),
		},
		Hostname:           "first",
		WireguardPublicKey: randomPubKey(t),
	}); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Second redemption must be rejected.
	_, err = f.coord.Register(context.Background(), &bamboov1.RegisterRequest{
		Credential: &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: created.GetSecret(),
		},
		Hostname:           "second",
		WireguardPublicKey: randomPubKey(t),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("err code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestPreAuthKey_MalformedSecret(t *testing.T) {
	f := startFixture(t)

	_, err := f.coord.Register(context.Background(), &bamboov1.RegisterRequest{
		Credential: &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: "not-a-valid-key",
		},
		Hostname:           "x",
		WireguardPublicKey: randomPubKey(t),
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("err code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestPreAuthKey_ListAndRevoke(t *testing.T) {
	f := startFixture(t)
	ctx := f.outgoingCtx(context.Background())

	created, err := f.auth.CreatePreAuthKey(ctx, &bamboov1.CreatePreAuthKeyRequest{
		Description: "list-and-revoke",
		Reusable:    true,
	})
	if err != nil {
		t.Fatalf("CreatePreAuthKey: %v", err)
	}

	listed, err := f.auth.ListPreAuthKeys(ctx, &bamboov1.ListPreAuthKeysRequest{})
	if err != nil {
		t.Fatalf("ListPreAuthKeys: %v", err)
	}
	if got := len(listed.GetKeys()); got != 1 {
		t.Fatalf("listed %d keys, want 1", got)
	}
	if listed.GetKeys()[0].GetId() != created.GetKey().GetId() {
		t.Errorf("listed id = %s, want %s",
			listed.GetKeys()[0].GetId(), created.GetKey().GetId())
	}

	if _, err := f.auth.RevokePreAuthKey(ctx, &bamboov1.RevokePreAuthKeyRequest{
		Id: created.GetKey().GetId(),
	}); err != nil {
		t.Fatalf("RevokePreAuthKey: %v", err)
	}

	// After revocation, redemption must fail.
	_, err = f.coord.Register(context.Background(), &bamboov1.RegisterRequest{
		Credential: &bamboov1.RegisterRequest_PreAuthKeySecret{
			PreAuthKeySecret: created.GetSecret(),
		},
		Hostname:           "x",
		WireguardPublicKey: randomPubKey(t),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("post-revoke err code = %v, want PermissionDenied", status.Code(err))
	}
}
