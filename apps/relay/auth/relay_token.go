// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth verifies controller-issued relay tokens. The relay
// binary is a separate Go module, so it cannot import
// apps/controller/internal/auth directly. We vendor the small
// verify-only path here. Token issuance lives only in the controller.
//
// Wire format: <base64url(json claims)>.<base64url(hmac-sha256(claims))>
// Claims: { tid: <tenant uuid>, pid: <peer uuid>, wg: <pubkey>,
//
//	iat: <unix>, exp: <unix> }
package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidToken collapses every verification failure so we never
// leak validation details over the wire.
var ErrInvalidToken = errors.New("invalid relay token")

// relayTokenDomain is mixed into the HMAC input, matching the
// controller's IssueRelayToken. It makes a relay token cryptographically
// distinct from a session / peer-session token even under a shared
// signing secret (the audit's H-1 token-confusion fix). This value MUST
// stay byte-identical to relayTokenDomain in
// apps/controller/internal/auth/relay_token.go — if they drift, every
// controller-issued relay token stops verifying here.
const relayTokenDomain = "bamboo.relay-token.v1"

// RelayClaims is the verified payload. Field names match the JSON
// emitted by apps/controller/internal/auth.IssueRelayToken.
type RelayClaims struct {
	TenantID    string `json:"tid"`
	PeerID      string `json:"pid"`
	WGPublicKey string `json:"wg"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
}

// VerifyRelayToken parses and validates a controller-issued relay
// token. On success returns the embedded claims; failures collapse
// to ErrInvalidToken so the caller never leaks validation details.
func VerifyRelayToken(secret []byte, token string) (*RelayClaims, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	expectedMAC := hmac.New(sha256.New, secret)
	expectedMAC.Write([]byte(relayTokenDomain))
	expectedMAC.Write([]byte(body))
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if !hmac.Equal(gotSig, expectedMAC.Sum(nil)) {
		return nil, ErrInvalidToken
	}
	rawBody, err := base64.RawURLEncoding.DecodeString(body)
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

// VerifyRelayTokenEd25519 validates an Ed25519-signed relay token against
// the controller's public key. Mirrors the controller's
// IssueRelayTokenEd25519 — it verifies ed25519(relayTokenDomain || body).
// The relay holds ONLY the public key, so a relay-host compromise can't
// forge tokens (audit C-1 root fix).
func VerifyRelayTokenEd25519(pub ed25519.PublicKey, token string) (*RelayClaims, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrInvalidToken
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	rawSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if !ed25519.Verify(pub, []byte(relayTokenDomain+body), rawSig) {
		return nil, ErrInvalidToken
	}
	rawBody, err := base64.RawURLEncoding.DecodeString(body)
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

// ParseRelayPublicKey decodes the controller's base64 Ed25519 public key
// (BAMBOO_RELAY_PUBLIC_KEY). MUST use the same base64 flavor as the
// controller's key encoding (std, padded) — see relayKeyEncoding in
// apps/controller/internal/auth/relay_token.go.
func ParseRelayPublicKey(b64 string) (ed25519.PublicKey, error) {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("decode relay public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("relay public key: want %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	return ed25519.PublicKey(pub), nil
}
