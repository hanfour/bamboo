// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

// TestGenerateAPIToken_RoundTrip pins the format contract: the
// plaintext starts with bat_, embeds the id (parseable by
// ParseAPIToken), and the returned bcrypt hash verifies against
// the plaintext.
func TestGenerateAPIToken_RoundTrip(t *testing.T) {
	id := uuid.New()
	plaintext, hash, err := auth.GenerateAPIToken(id)
	if err != nil {
		t.Fatalf("GenerateAPIToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, auth.APITokenPrefix) {
		t.Errorf("plaintext missing prefix: %q", plaintext)
	}
	gotID, err := auth.ParseAPIToken(plaintext)
	if err != nil {
		t.Fatalf("ParseAPIToken: %v", err)
	}
	if gotID != id {
		t.Errorf("parsed id = %v, want %v", gotID, id)
	}
	if err := auth.VerifyHash(plaintext, hash); err != nil {
		t.Errorf("VerifyHash on round-trip plaintext: %v", err)
	}
	// A different plaintext must fail the hash check — sanity that
	// the bcrypt comparator isn't matching everything.
	if err := auth.VerifyHash(plaintext+"x", hash); err == nil {
		t.Error("VerifyHash accepted tampered plaintext")
	}
}

// TestGenerateAPIToken_DistinctEachCall verifies the random
// suffix actually varies between calls — a non-random suffix
// would make all tokens for the same id identical.
func TestGenerateAPIToken_DistinctEachCall(t *testing.T) {
	id := uuid.New()
	first, _, err := auth.GenerateAPIToken(id)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, _, err := auth.GenerateAPIToken(id)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first == second {
		t.Errorf("two mint calls for same id produced identical plaintext: %q", first)
	}
}

// TestParseAPIToken_RejectsMalformed covers the classifier path
// the authn middleware depends on: anything that isn't a valid
// bat_ token gets ErrMalformedSecret so the middleware falls
// through to the session-JWT verifier.
func TestParseAPIToken_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not-a-token",
		"bka_abc_def",                // wrong prefix (pre-auth key)
		"bat_",                       // bare prefix
		"bat_nounderscore",           // missing _
		"bat_!!!!_xxxx",              // non-base32 id
		"bat_aaaaaaaaaaaaaaaa_xxxxx", // wrong-length id (12 bytes vs 16)
	}
	for _, c := range cases {
		if _, err := auth.ParseAPIToken(c); err == nil {
			t.Errorf("ParseAPIToken(%q) should fail; got nil", c)
		}
	}
}
