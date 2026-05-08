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

// Loop the tamper case 200× to make sure the fix above is deterministic
// against random sig material, not just lucky.
func TestSessionToken_TamperedSignature_Stress(t *testing.T) {
	secret := []byte("test-secret-for-hmac")
	for i := 0; i < 200; i++ {
		tok, err := auth.IssueSessionToken(secret, auth.SessionClaims{
			UserID:   uuid.New(),
			TenantID: uuid.New(),
		}, time.Hour)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		dot := strings.IndexByte(tok, '.')
		b := []byte(tok)
		if b[dot+1] == 'A' {
			b[dot+1] = 'B'
		} else {
			b[dot+1] = 'A'
		}
		if _, err := auth.VerifySessionToken(secret, string(b)); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("iter %d: got err %v, want ErrInvalidToken", i, err)
		}
	}
}

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

	// Tamper the FIRST byte of the signature (after the '.'). Tampering
	// the last byte was flaky: a 32-byte HMAC encodes to 43 base64 chars
	// where the trailing char carries only 4 meaningful bits, so randomly
	// landing on the same character (or a non-strict-decode equivalent)
	// produced an unchanged sig.
	dot := strings.IndexByte(tok, '.')
	if dot < 0 || dot+1 >= len(tok) {
		t.Fatalf("token has no signature segment: %q", tok)
	}
	b := []byte(tok)
	if b[dot+1] == 'A' {
		b[dot+1] = 'B'
	} else {
		b[dot+1] = 'A'
	}
	tampered := string(b)

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
