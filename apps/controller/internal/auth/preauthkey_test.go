// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
)

func TestGenerateAndParseRoundTrip(t *testing.T) {
	id := uuid.New()

	plaintext, hash, err := auth.GenerateSecret(id)
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}

	if !strings.HasPrefix(plaintext, auth.SecretPrefix) {
		t.Errorf("plaintext does not start with prefix: %q", plaintext)
	}
	if hash == plaintext {
		t.Error("hash should not equal plaintext")
	}

	parsed, err := auth.ParseSecret(plaintext)
	if err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
	if parsed != id {
		t.Errorf("parsed id = %s, want %s", parsed, id)
	}

	if err := auth.VerifyHash(plaintext, hash); err != nil {
		t.Errorf("VerifyHash: %v", err)
	}
}

func TestVerifyHash_wrongSecret(t *testing.T) {
	id := uuid.New()
	plaintext, hash, err := auth.GenerateSecret(id)
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}

	if err := auth.VerifyHash(plaintext+"x", hash); err == nil {
		t.Error("expected verify error for tampered secret")
	}
}

func TestParseSecret_malformed(t *testing.T) {
	cases := []string{
		"",
		"not-a-secret",
		"bka_only-prefix",        // no underscore separator after key id
		"bka_invalidbase32!_xyz", // invalid base32 characters
		"bka_aaaa_xyz",           // base32 too short to decode to 16 bytes
		"wrong_prefix_xyz",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := auth.ParseSecret(c)
			if !errors.Is(err, auth.ErrMalformedSecret) {
				t.Errorf("got err %v, want ErrMalformedSecret", err)
			}
		})
	}
}
