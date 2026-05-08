// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth contains primitives for authenticating peers and humans.
//
// PreAuthKey secret format:
//
//	bka_<base32(key_id)>_<base32(random)>
//
// The key_id prefix gives the controller an O(1) lookup; the random suffix
// is the actual entropy. The DB stores only a bcrypt hash of the secret.
// On redemption, the controller parses the prefix, fetches the row, then
// uses bcrypt.CompareHashAndPassword in constant time.
package auth

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// SecretPrefix is prepended to every PreAuthKey secret. It distinguishes
// our keys from other tokens at a glance and is checked when parsing.
const SecretPrefix = "bka_"

// b32 is base32 lowercase, no padding — friendly to copy/paste and shells.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// ErrMalformedSecret is returned when a secret cannot be parsed.
var ErrMalformedSecret = errors.New("malformed pre-auth key secret")

// GenerateSecret produces a PreAuthKey secret tied to the given key id.
// It returns the plaintext (to be shown to the user once) and the bcrypt
// hash to be persisted.
func GenerateSecret(keyID uuid.UUID) (plaintext, hash string, err error) {
	const randomBytes = 24
	rnd := make([]byte, randomBytes)
	if _, err := rand.Read(rnd); err != nil {
		return "", "", fmt.Errorf("read random: %w", err)
	}

	encodedID := strings.ToLower(b32.EncodeToString(keyID[:]))
	encodedRnd := strings.ToLower(b32.EncodeToString(rnd))
	plaintext = SecretPrefix + encodedID + "_" + encodedRnd

	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("bcrypt: %w", err)
	}
	return plaintext, string(h), nil
}

// ParseSecret extracts the embedded key id from a presented secret.
// It does not verify the hash; callers must follow up with VerifyHash.
func ParseSecret(plaintext string) (uuid.UUID, error) {
	rest, ok := strings.CutPrefix(plaintext, SecretPrefix)
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

// VerifyHash checks plaintext against a stored bcrypt hash. It is constant
// time. Returns nil on match.
func VerifyHash(plaintext, storedHash string) error {
	return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(plaintext))
}
