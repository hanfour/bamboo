// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SessionClaims is the payload we put inside a session JWT. The format is
// our own minimal JSON; we sign it with HMAC-SHA256.
//
// HMAC-signed (rather than RS256) keeps Phase 1 simple: no JWKS rotation,
// no public-key distribution. A future ADR will move to asymmetric signing
// once we have multi-tenant verifier requirements (e.g. third-party SDKs).
type SessionClaims struct {
	UserID    uuid.UUID `json:"sub"`
	TenantID  uuid.UUID `json:"tid"`
	IssuedAt  int64     `json:"iat"`
	ExpiresAt int64     `json:"exp"`
	// JTI is the per-session identifier the revocation denylist
	// keys off (see migration 00016). Zero (uuid.Nil) on tokens
	// minted before the slice-3a rollout — the auth middleware
	// treats a zero jti as "no revocation check possible" rather
	// than rejecting, so in-flight legacy tokens stay valid until
	// their natural TTL.
	JTI uuid.UUID `json:"jti,omitempty"`
}

// ErrInvalidToken is returned by VerifySessionToken on any failure.
var ErrInvalidToken = errors.New("invalid session token")

// IssueSessionToken returns a signed token for the supplied claims.
// The TTL is added to time.Now() if claims.ExpiresAt is zero. A
// zero JTI is replaced with a fresh UUIDv4 so every issued token
// gets a unique identifier the revocation denylist can target.
func IssueSessionToken(secret []byte, claims SessionClaims, ttl time.Duration) (string, error) {
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = time.Now().Add(ttl).Unix()
	}
	if claims.JTI == uuid.Nil {
		claims.JTI = uuid.New()
	}

	body, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	encoded := base64Encode(body)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encoded))
	sig := base64Encode(mac.Sum(nil))

	return encoded + "." + sig, nil
}

// VerifySessionToken parses and validates a token. On success it returns
// the embedded claims. Failures collapse into ErrInvalidToken so callers
// never leak validation details to peers.
func VerifySessionToken(secret []byte, token string) (*SessionClaims, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}

	expectedMAC := hmac.New(sha256.New, secret)
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
	var claims SessionClaims
	if err := json.Unmarshal(rawBody, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}

func base64Encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func base64Decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
