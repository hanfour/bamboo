// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth verifies controller-issued relay tokens. The relay
// binary is a separate Go module, so it cannot import
// apps/controller/internal/auth directly. We vendor the small
// verify-only path here. Token issuance lives only in the controller.
//
// Wire format: <base64url(json claims)>.<base64url(hmac-sha256(claims))>
// Claims: { tid: <tenant uuid>, pid: <peer uuid>, wg: <pubkey>,
//           iat: <unix>, exp: <unix> }
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrInvalidToken collapses every verification failure so we never
// leak validation details over the wire.
var ErrInvalidToken = errors.New("invalid relay token")

// RelayClaims is the verified payload. Field names match the JSON
// emitted by apps/controller/internal/auth.IssueRelayToken.
type RelayClaims struct {
	TenantID    string `json:"tid"`
	PeerID      string `json:"pid"`
	WGPublicKey string `json:"wg"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
}

func VerifyRelayToken(secret []byte, token string) (*RelayClaims, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	expectedMAC := hmac.New(sha256.New, secret)
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
