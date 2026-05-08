// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func TestSessionToken_RoundTrip(t *testing.T) {
	secret := []byte("test-secret-for-hmac")
	user := uuid.New()
	tenant := uuid.New()

	tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   user,
		TenantID: tenant,
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := auth.VerifySessionToken(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.UserID != user {
		t.Errorf("UserID = %s, want %s", claims.UserID, user)
	}
	if claims.TenantID != tenant {
		t.Errorf("TenantID = %s, want %s", claims.TenantID, tenant)
	}
}

func TestSessionToken_TamperedSignature(t *testing.T) {
	secret := []byte("test-secret-for-hmac")
	tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: uuid.New(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	tampered := tok[:len(tok)-1] + "X"
	if _, err := auth.VerifySessionToken(secret, tampered); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("got err %v, want ErrInvalidToken", err)
	}
}

func TestSessionToken_WrongSecret(t *testing.T) {
	tok, err := auth.IssueSessionToken([]byte("secret-A"), auth.SessionClaims{
		UserID:   uuid.New(),
		TenantID: uuid.New(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := auth.VerifySessionToken([]byte("secret-B"), tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("got err %v, want ErrInvalidToken", err)
	}
}

func TestSessionToken_Expired(t *testing.T) {
	secret := []byte("test-secret")
	tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
		UserID:    uuid.New(),
		TenantID:  uuid.New(),
		IssuedAt:  time.Now().Add(-2 * time.Hour).Unix(),
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := auth.VerifySessionToken(secret, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("got err %v, want ErrInvalidToken", err)
	}
}

func TestSessionToken_Malformed(t *testing.T) {
	secret := []byte("s")
	cases := []string{
		"",
		"no-dot",
		"a.b.c",
		strings.Repeat("a", 100),
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := auth.VerifySessionToken(secret, c); !errors.Is(err, auth.ErrInvalidToken) {
				t.Errorf("got err %v, want ErrInvalidToken", err)
			}
		})
	}
}
