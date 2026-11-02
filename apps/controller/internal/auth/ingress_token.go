// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// IngressRole is the only role an ingress service token carries.
const IngressRole = "ingress"

// ingressTokenDomain is mixed into the signed bytes so an ingress token
// never verifies as a relay or session token, even under a shared key.
const ingressTokenDomain = "bamboo.ingress-token.v1"

// IngressClaims authenticate the bamboo-ingress process to the
// controller's /internal/v1/ingress/* endpoints. PeerID is the ingress
// peer. JTI must equal the tenant's row in ingress_tokens; issuing a new
// token replaces it, which revokes the old one.
type IngressClaims struct {
	TenantID  uuid.UUID `json:"tid"`
	PeerID    uuid.UUID `json:"pid"`
	Role      string    `json:"role"`
	JTI       uuid.UUID `json:"jti"`
	IssuedAt  int64     `json:"iat"`
	ExpiresAt int64     `json:"exp"`
}

// IssueIngressToken signs claims with Ed25519. Wire format matches relay
// tokens: <base64url(json)>.<base64url(sig)>.
func IssueIngressToken(priv ed25519.PrivateKey, claims IngressClaims, ttl time.Duration) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("ingress signing key: want %d bytes, got %d", ed25519.PrivateKeySize, len(priv))
	}
	if claims.Role != IngressRole {
		return "", fmt.Errorf("ingress token role must be %q", IngressRole)
	}
	now := time.Now()
	if claims.IssuedAt == 0 {
		claims.IssuedAt = now.Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = now.Add(ttl).Unix()
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal ingress claims: %w", err)
	}
	encoded := base64Encode(body)
	sig := ed25519.Sign(priv, []byte(ingressTokenDomain+encoded))
	return encoded + "." + base64Encode(sig), nil
}

// VerifyIngressToken checks signature, role, ids, and expiry. Every
// failure is ErrInvalidToken. Revocation (jti) is the caller's check.
func VerifyIngressToken(pub ed25519.PublicKey, token string) (*IngressClaims, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrInvalidToken
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	rawSig, err := base64Decode(sig)
	if err != nil || !ed25519.Verify(pub, []byte(ingressTokenDomain+body), rawSig) {
		return nil, ErrInvalidToken
	}
	rawBody, err := base64Decode(body)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var claims IngressClaims
	if err := json.Unmarshal(rawBody, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if claims.Role != IngressRole || claims.TenantID == uuid.Nil || claims.PeerID == uuid.Nil || claims.JTI == uuid.Nil {
		return nil, ErrInvalidToken
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}
