// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
// The signed bytes always mix in relayTokenDomain so a relay token can
// never be verified as a SessionToken (and vice versa) even under a
// shared HMAC secret — the audit's H-1 token-confusion fix.
//
// Two signing schemes are supported, chosen by the deployment's config:
//   - HMAC-SHA256 (IssueRelayToken / VerifyRelayToken): the relay holds
//     the same secret. Simple, but a relay-host compromise leaks a key
//     that can forge tokens (audit C-1).
//   - Ed25519 (IssueRelayTokenEd25519 / VerifyRelayTokenEd25519): the
//     controller signs with a private key; the relay verifies with only
//     the public half, so a relay-host compromise can NOT forge tokens.
//     This is the C-1 root fix.
//
// The verifier is picked by config (public key set → Ed25519, else
// HMAC), so there is no in-band "alg" field and no alg-confusion surface.
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

// relaySignedBytes is the exact byte string signed by both schemes:
// relayTokenDomain || encoded. Keeping it in one place guarantees the
// HMAC and Ed25519 paths bind the domain identically.
func relaySignedBytes(encoded string) []byte {
	return []byte(relayTokenDomain + encoded)
}

// IssueRelayTokenEd25519 signs a relay token with an Ed25519 private key
// (audit C-1 root fix). Wire format is identical to the HMAC path —
// <base64url(json)>.<base64url(sig)> — only the signature algorithm
// differs; the relay verifies with just the public half.
func IssueRelayTokenEd25519(priv ed25519.PrivateKey, claims RelayClaims, ttl time.Duration) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("relay signing key: want %d bytes, got %d", ed25519.PrivateKeySize, len(priv))
	}
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
	sig := ed25519.Sign(priv, relaySignedBytes(encoded))
	return encoded + "." + base64Encode(sig), nil
}

// VerifyRelayTokenEd25519 validates a token against an Ed25519 public
// key. Failures collapse to ErrInvalidToken.
func VerifyRelayTokenEd25519(pub ed25519.PublicKey, token string) (*RelayClaims, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrInvalidToken
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	rawSig, err := base64Decode(sig)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if !ed25519.Verify(pub, relaySignedBytes(body), rawSig) {
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

// relayKeyEncoding is the base64 flavor used for the on-the-wire key
// material in config / .env / the keygen subcommand. StdEncoding (with
// padding) is the least-surprising for a value an operator copy-pastes.
var relayKeyEncoding = base64.StdEncoding

// ParseRelaySigningKey decodes a base64 Ed25519 seed (32 bytes) into a
// private key. Operators generate one with the controller's relaykey
// subcommand; empty input means "not configured" (fall back to HMAC).
func ParseRelaySigningKey(b64 string) (ed25519.PrivateKey, error) {
	seed, err := relayKeyEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("decode relay signing key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("relay signing key: want a %d-byte seed, got %d bytes", ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// ParseRelayPublicKey decodes a base64 Ed25519 public key (32 bytes).
func ParseRelayPublicKey(b64 string) (ed25519.PublicKey, error) {
	pub, err := relayKeyEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("decode relay public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("relay public key: want %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	return ed25519.PublicKey(pub), nil
}

// GenerateRelayKeypair returns a fresh Ed25519 keypair as base64 strings:
// the seed (the controller's BAMBOO_RELAY_SIGNING_KEY) and the public key
// (the relay's BAMBOO_RELAY_PUBLIC_KEY). Used by the relaykey subcommand.
func GenerateRelayKeypair() (seedB64, publicB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	return relayKeyEncoding.EncodeToString(priv.Seed()), relayKeyEncoding.EncodeToString(pub), nil
}
