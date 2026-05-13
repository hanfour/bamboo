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

// PeerSessionClaims is the payload of a peer-bound access token issued
// by the controller at Register time. A client presents it back to the
// controller for the peer-mutation endpoints (Heartbeat, WatchPeers,
// relay-token mint) so the controller can identify the calling peer
// without trusting an unauthenticated tenant slug.
//
// This is distinct from SessionClaims (user-bound, OIDC-derived) and
// RelayClaims (peer-bound, presented to the relay server, not the
// controller). The shapes are similar but the audiences differ: the
// relay token's audience is the relay binary, the peer session
// token's audience is the controller REST API. Reuse across audiences
// is intentionally not supported — claims with the wrong shape will
// fail JSON decoding.
//
// WGPublicKey is included so a token for a rotated peer (same id, new
// pubkey) fails verification before being trusted. PeerSessionScope is
// reserved for future bounded permissions (e.g. "may renew" vs "may
// not"); v1 has only the implicit "peer-self" scope.
type PeerSessionClaims struct {
	TenantID    uuid.UUID `json:"tid"`
	PeerID      uuid.UUID `json:"pid"`
	WGPublicKey string    `json:"wg"`
	IssuedAt    int64     `json:"iat"`
	ExpiresAt   int64     `json:"exp"`
}

// peerSessionDomain is mixed into the HMAC input so this token type
// is distinct from SessionToken / RelayToken even when they share the
// same signing secret. A user-session JWT or a relay-token presented
// as a peer-session bearer will fail HMAC equality because their
// signatures don't include the domain prefix. The existing token
// types don't apply the inverse prefix yet — that's a separate ADR
// (see TODO in jwt.go).
const peerSessionDomain = "bamboo.peer-session.v1"

// IssuePeerSessionToken signs a fresh peer session token. TTL should
// be short enough that a leaked token has bounded reuse, but long
// enough that long-lived CLI sessions don't re-register churn. 24h
// matches the existing OIDC session TTL and gives one renewal per day
// — the next Register call (or a later renewal path) rotates it.
func IssuePeerSessionToken(secret []byte, claims PeerSessionClaims, ttl time.Duration) (string, error) {
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = time.Now().Add(ttl).Unix()
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal peer session claims: %w", err)
	}
	encoded := base64Encode(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(peerSessionDomain))
	mac.Write([]byte(encoded))
	sig := base64Encode(mac.Sum(nil))
	return encoded + "." + sig, nil
}

// VerifyPeerSessionToken validates the token and returns the embedded
// claims. Failures collapse to ErrInvalidToken — the controller never
// surfaces validation details (HMAC mismatch vs malformed body vs
// expired) so probing callers cannot enumerate failure modes.
func VerifyPeerSessionToken(secret []byte, token string) (*PeerSessionClaims, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	expectedMAC := hmac.New(sha256.New, secret)
	expectedMAC.Write([]byte(peerSessionDomain))
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
	var claims PeerSessionClaims
	if err := json.Unmarshal(rawBody, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	// Reject claims missing either id — a token whose JSON parsed but
	// has zero uuids cannot identify a peer and must not be trusted.
	if claims.TenantID == uuid.Nil || claims.PeerID == uuid.Nil {
		return nil, ErrInvalidToken
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}
