// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func ingressKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func validIngressClaims() auth.IngressClaims {
	return auth.IngressClaims{TenantID: uuid.New(), PeerID: uuid.New(), Role: auth.IngressRole, JTI: uuid.New()}
}

func TestIngressToken_RoundTrip(t *testing.T) {
	pub, priv := ingressKey(t)
	in := validIngressClaims()
	tok, err := auth.IssueIngressToken(priv, in, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := auth.VerifyIngressToken(pub, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TenantID != in.TenantID || got.PeerID != in.PeerID || got.JTI != in.JTI || got.Role != "ingress" || got.ExpiresAt <= time.Now().Unix() {
		t.Errorf("claims = %+v", got)
	}
}

func TestIngressToken_Rejects(t *testing.T) {
	pub, priv := ingressKey(t)
	otherPub, _ := ingressKey(t)
	good, _ := auth.IssueIngressToken(priv, validIngressClaims(), time.Hour)

	expiredClaims := validIngressClaims()
	expiredClaims.IssuedAt = time.Now().Add(-2 * time.Hour).Unix()
	expiredClaims.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	expired, _ := auth.IssueIngressToken(priv, expiredClaims, 0)

	// A relay token signed by the same key must not verify as an ingress
	// token (domain separation).
	relay, _ := auth.IssueRelayTokenEd25519(priv, auth.RelayClaims{TenantID: uuid.New(), PeerID: uuid.New()}, time.Hour)

	body, sig, _ := strings.Cut(good, ".")
	tampered := body + "x." + sig

	for name, tc := range map[string]struct {
		pub ed25519.PublicKey
		tok string
	}{
		"wrong key":   {otherPub, good},
		"expired":     {pub, expired},
		"relay token": {pub, relay},
		"tampered":    {pub, tampered},
		"garbage":     {pub, "not-a-token"},
	} {
		if _, err := auth.VerifyIngressToken(tc.pub, tc.tok); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: err=%v, want ErrInvalidToken", name, err)
		}
	}

	wrongRole := validIngressClaims()
	wrongRole.Role = "relay"
	if _, err := auth.IssueIngressToken(priv, wrongRole, time.Hour); err == nil {
		t.Error("issuing with a non-ingress role must fail")
	}
}
