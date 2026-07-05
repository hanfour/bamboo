// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RelayClaims is the payload of a relay-server access token. The
// relay binary verifies these claims to:
//
//  1. Confirm the client is allowed to use the relay at all
//     (signed by the controller's shared secret).
//  2. Partition routing tables per tenant_id so cross-tenant traffic
//     is impossible.
//  3. Match wg_public_key against the CLIENT_HELLO so the client
//     can't impersonate another peer.
//
// HMAC signing mixes in relayTokenDomain so a relay token can never be
// verified as a SessionToken (and vice versa) even under a shared
// signing secret — the audit's H-1 token-confusion fix. A future ADR
// will move to asymmetric signing so the relay only needs the public
// half.
type RelayClaims struct {
	TenantID    uuid.UUID `json:"tid"`
	PeerID      uuid.UUID `json:"pid"`
	WGPublicKey string    `json:"wg"`
	IssuedAt    int64     `json:"iat"`
	ExpiresAt   int64     `json:"exp"`
}

// relayTokenDomain is mixed into the HMAC input so this token type is
// cryptographically distinct from SessionToken / OIDCState even when
// they share the signing secret. It mirrors peerSessionDomain (see
// peer_session_token.go). The relay binary vendors a copy of the verify
// path in apps/relay/auth — this constant MUST stay byte-identical to
// the relayTokenDomain there, or controller-issued tokens stop
// verifying at the relay.
const relayTokenDomain = "bamboo.relay-token.v1"

// IssueRelayToken signs a fresh token. Use a short TTL (e.g. 1 hour);
// clients should call /api/v1/relay-tokens to refresh.
func IssueRelayToken(secret []byte, claims RelayClaims, ttl time.Duration) (string, error) {
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = time.Now().Add(ttl).Unix()
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal relay claims: %w", err)
	}
	encoded := base64Encode(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(relayTokenDomain))
	mac.Write([]byte(encoded))
	sig := base64Encode(mac.Sum(nil))
	return encoded + "." + sig, nil
}

// VerifyRelayToken validates the token and returns the embedded claims.
// Failures collapse to ErrInvalidToken so the relay never leaks
// validation details over the wire.
func VerifyRelayToken(secret []byte, token string) (*RelayClaims, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	expectedMAC := hmac.New(sha256.New, secret)
	expectedMAC.Write([]byte(relayTokenDomain))
	expectedMAC.Write([]byte(body))
	expectedSig := base64Encode(expectedMAC.Sum(nil))
	gotSig, err := base64Decode(sig)
	if err != nil {
		return nil, ErrInvalidToken
	}
	wantSig, err := base64Decode(expectedSig)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if !hmac.Equal(gotSig, wantSig) {
		return nil, ErrInvalidToken
	}
	rawBody, err := base64Decode(body)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var claims RelayClaims
	if err := json.Unmarshal(rawBody, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}
