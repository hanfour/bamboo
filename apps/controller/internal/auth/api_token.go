// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// APITokenPrefix is prepended to every API-token plaintext. The
// distinctive 4-char prefix lets the bearer-token classifier in
// the HTTP middleware route to the API-token verification path
// instead of the session-JWT path without a DB lookup, and lets
// a leaked token be classified at a glance.
//
// Format mirrors the pre-auth-key token (see GenerateSecret):
//
//	bat_<base32lower(token_id)>_<base32lower(random)>
//
// The embedded token_id gives the authn middleware an O(1) row
// lookup; the random suffix is the actual entropy. The DB stores
// only a bcrypt hash of the full plaintext, so a leaked database
// snapshot doesn't yield usable tokens.
const APITokenPrefix = "bat_"

// GenerateAPIToken produces an API-token plaintext tied to the
// given token id, returning the plaintext (shown to the minter
// once) and the bcrypt hash to persist. Same 24 bytes of entropy
// as the pre-auth-key path; bcrypt.DefaultCost (currently 10)
// matches what the other secret-bearing repos use so a rotation
// of the cost is one place to edit later.
func GenerateAPIToken(tokenID uuid.UUID) (plaintext, hash string, err error) {
	const randomBytes = 24
	rnd := make([]byte, randomBytes)
	if _, err := rand.Read(rnd); err != nil {
		return "", "", fmt.Errorf("read random: %w", err)
	}

	encodedID := strings.ToLower(b32.EncodeToString(tokenID[:]))
	encodedRnd := strings.ToLower(b32.EncodeToString(rnd))
	plaintext = APITokenPrefix + encodedID + "_" + encodedRnd

	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("bcrypt: %w", err)
	}
	return plaintext, string(h), nil
}

// ParseAPIToken extracts the embedded token id from a presented
// API-token plaintext. Returns ErrMalformedSecret when the prefix
// or shape doesn't match — the authn middleware uses this as the
// classifier signal: anything that fails to parse falls through to
// the session-JWT path so a malformed session bearer doesn't
// mis-route as a malformed API token.
//
// Does NOT verify the bcrypt hash; the caller fetches the row by
// the returned id, then calls VerifyHash with the same plaintext.
func ParseAPIToken(plaintext string) (uuid.UUID, error) {
	rest, ok := strings.CutPrefix(plaintext, APITokenPrefix)
	if !ok {
		return uuid.Nil, ErrMalformedSecret
	}
	encodedID, _, ok := strings.Cut(rest, "_")
	if !ok {
		return uuid.Nil, ErrMalformedSecret
	}
	idBytes, err := b32.DecodeString(strings.ToUpper(encodedID))
	if err != nil || len(idBytes) != 16 {
		return uuid.Nil, ErrMalformedSecret
	}
	var id uuid.UUID
	copy(id[:], idBytes)
	return id, nil
}
